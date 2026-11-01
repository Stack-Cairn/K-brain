package backend

import (
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/approval"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const (
	ruleAllowReason     = "allowed by a remembered permission rule"
	readOnlyAllowReason = "read-only action"
)

// sessionRulesPath keeps session-scope rules beside the session's event journal, so
// deleting the session deletes them.
func (s *Server) sessionRulesPath(id string) string {
	return filepath.Join(s.eventDir, id+".permission-rules.json")
}

// projectRootFor is the project a run's project-scope rules belong to: its first
// writable workspace root, else the session's working directory.
func projectRootFor(options protocol.RunOptions, workingDir string) string {
	for _, root := range options.WorkspaceRoots {
		if root.Access == "write" && filepath.IsAbs(root.Path) {
			return filepath.Clean(root.Path)
		}
	}
	if filepath.IsAbs(workingDir) {
		return filepath.Clean(workingDir)
	}
	return ""
}

// ruleSets reads the session, project and user rules. User and project rules need
// PermissionRulesDir; without it only session rules apply.
func (s *Server) ruleSets(sessionID, root string) ([]permissions.RuleSet, error) {
	var sets []permissions.RuleSet
	var errs []error
	add := func(set permissions.RuleSet, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		sets = append(sets, set)
	}
	add(permissions.LoadRuleSet(s.sessionRulesPath(sessionID), permissions.ScopeSession))
	if s.permissionDir != "" {
		if root != "" {
			if path, err := permissions.ProjectRulesPathIn(s.permissionDir, root); err == nil {
				add(permissions.LoadRuleSet(path, permissions.ScopeProject))
			}
		}
		add(permissions.LoadUserRulesIn(s.permissionDir))
	}
	return sets, errors.Join(errs...)
}

// evaluateRules decides a gated call from the remembered rules. When a rules file
// cannot be read its deny rules cannot be checked, so the call must be asked about.
//
// A review attached to the call limits what may skip the prompt: any finding turns
// off the read-only allowance, and a concrete finding also sets remembered rules
// aside (a merely cautious review lets them stand). Deny rules always count.
func (s *Server) evaluateRules(sessionID, root string, req tools.GateRequest) permissions.Decision {
	sets, err := s.ruleSets(sessionID, root)
	decision := permissions.Evaluate(permissions.Request{Tool: req.Tool, Command: req.Command, Shell: req.Shell, Poll: req.Poll, OtherDir: req.OtherDir}, permissions.Options{ProjectRoot: root, AllowReadOnly: approval.ReadOnlyUsable(req.Context)}, sets...)
	if err != nil && decision.Effect != permissions.Deny {
		return permissions.Decision{Effect: permissions.Ask}
	}
	if decision.Effect == permissions.Allow && decision.Source == permissions.SourceRule && !approval.RulesUsable(req.Context) {
		return permissions.Decision{}
	}
	return decision
}

// suggestionFor is what allow_always would remember for req, or nil. The default
// scope is one this backend can store.
func (s *Server) suggestionFor(req tools.GateRequest, root string) *protocol.PermissionSuggestion {
	rules := permissions.Suggest(permissions.Request{Tool: req.Tool, Command: req.Command, Shell: req.Shell, Poll: req.Poll, OtherDir: req.OtherDir}, permissions.Options{ProjectRoot: root, AllowReadOnly: true})
	if len(rules) == 0 {
		return nil
	}
	scope := string(permissions.ScopeProject)
	if root == "" || s.permissionDir == "" {
		scope = string(permissions.ScopeSession)
	}
	return &protocol.PermissionSuggestion{Rules: rules, DefaultScope: scope}
}

// legacyRememberable reports whether an allow_always without a scope may store the
// suggestion. Clients from before rules show the prompt's rule as what they allow
// for the session, so only a single rule matching it is stored; anything wider is
// allowed once.
func legacyRememberable(request protocol.PermissionRequest) bool {
	if request.Suggestion == nil || len(request.Suggestion.Rules) != 1 || request.Rule == "" {
		return false
	}
	rule, err := permissions.ParseRule(request.Suggestion.Rules[0])
	return err == nil && rule.Prefix && rule.Tool == "Bash" && rule.Pattern == request.Rule
}

// rulesPath is the file of one scope. Project scope needs root; session scope a
// valid session id.
func (s *Server) rulesPath(scope permissions.Scope, sessionID, root string) (string, error) {
	switch scope {
	case permissions.ScopeSession:
		if !validID(sessionID) {
			return "", errors.New("session scope needs a session id")
		}
		if s.store != nil {
			found, err := s.store.Exists([]string{sessionID})
			if err != nil {
				return "", err
			}
			if !found[sessionID] {
				return "", errors.New("session not found")
			}
		}
		return s.sessionRulesPath(sessionID), nil
	case permissions.ScopeProject:
		if s.permissionDir == "" {
			return "", errors.New("project rules are not available on this backend")
		}
		return permissions.ProjectRulesPathIn(s.permissionDir, root)
	case permissions.ScopeUser:
		if s.permissionDir == "" {
			return "", errors.New("user rules are not available on this backend")
		}
		return permissions.UserRulesPathIn(s.permissionDir), nil
	}
	return "", errors.New("scope must be session, project or user")
}

// rememberSuggestion stores the rules a permission request suggested in scope. An
// empty scope is this session only: before rules existed allow_always meant that,
// and clients that do not send a scope still say so to the user.
func (s *Server) rememberSuggestion(rt *runtimeSession, waiter *permissionWaiter, scope string) error {
	if scope == "" {
		if !legacyRememberable(waiter.request) {
			return nil
		}
		scope = string(permissions.ScopeSession)
	}
	path, err := s.rulesPath(permissions.Scope(scope), rt.id, waiter.root)
	if err != nil {
		return err
	}
	projectRoot := ""
	if permissions.Scope(scope) == permissions.ScopeProject {
		projectRoot = waiter.root
	}
	for _, rule := range waiter.request.Suggestion.Rules {
		if _, err := permissions.AddRule(path, projectRoot, permissions.StoredRule{Rule: rule, Effect: permissions.Allow, SourceSession: rt.id}); err != nil {
			return err
		}
	}
	return nil
}

// settleCoveredPermissions releases the session's other waiting prompts that the
// rules now cover, as when parallel tool calls asked about the same command. Prompts
// a review sent to a person are left alone unless the review was only cautious.
func (s *Server) settleCoveredPermissions(rt *runtimeSession) {
	rt.mu.Lock()
	waiting := make(map[string]*permissionWaiter, len(rt.permissions))
	for id, waiter := range rt.permissions {
		if waiter.ruleSettleable {
			waiting[id] = waiter
		}
	}
	rt.mu.Unlock()
	for id, waiter := range waiting {
		decision := s.evaluateRules(rt.id, waiter.root, waiter.gate)
		if decision.Effect != permissions.Allow || decision.Source != permissions.SourceRule {
			continue
		}
		rt.mu.Lock()
		current := rt.permissions[id]
		if current == waiter {
			delete(rt.permissions, id)
		}
		rt.mu.Unlock()
		if current != waiter {
			continue
		}
		select {
		case waiter.reason <- ruleAllowReason + " " + decision.Rule:
		default:
		}
		waiter.result <- tools.GateAllowOnce
	}
}

func (s *Server) publishAutoResolved(rt *runtimeSession, req tools.GateRequest, decision permissions.Decision) {
	payload := protocol.PermissionAutoResolved{ToolCallID: tools.ToolCallID(req.Context), Tool: req.Tool, Command: req.Command, Source: string(decision.Source), Rule: decision.Rule, Scope: string(decision.Scope)}
	_, _ = rt.publish(protocol.EventPermissionAutoResolved, payload, "")
}

// permissionRules serves /v1/permissions/rules: GET lists every scope the query
// names, POST adds a rule, DELETE /v1/permissions/rules/{id} removes one.
func (s *Server) permissionRules(w http.ResponseWriter, r *http.Request) {
	const prefix = "/v1/permissions/rules"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	query := r.URL.Query()
	workspace := strings.TrimSpace(query.Get("workspace"))
	sessionID := query.Get("session_id")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		out := protocol.PermissionRulesResponse{Rules: []protocol.PermissionRule{}}
		list := func(scope permissions.Scope, path string) error {
			set, err := permissions.LoadRuleSet(path, scope)
			if err != nil {
				return err
			}
			for _, rule := range set.Rules {
				out.Rules = append(out.Rules, protocol.PermissionRule{ID: rule.ID, Rule: rule.Rule, Effect: string(rule.Effect), Scope: string(scope), CreatedAt: rule.CreatedAt.UTC().Format(time.RFC3339), SourceSession: rule.SourceSession})
			}
			return nil
		}
		var errs []error
		if sessionID != "" {
			path, err := s.rulesPath(permissions.ScopeSession, sessionID, "")
			if err == nil {
				err = list(permissions.ScopeSession, path)
			}
			errs = append(errs, err)
		}
		if workspace != "" && s.permissionDir != "" {
			path, err := s.rulesPath(permissions.ScopeProject, "", workspace)
			if err == nil {
				out.ProjectRoot = filepath.Clean(workspace)
				err = list(permissions.ScopeProject, path)
			}
			errs = append(errs, err)
		}
		if s.permissionDir != "" {
			path, _ := s.rulesPath(permissions.ScopeUser, "", "")
			errs = append(errs, list(permissions.ScopeUser, path))
		}
		if err := errors.Join(errs...); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case rest == "" && r.Method == http.MethodPost:
		var in protocol.PermissionRuleCreate
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		scope := permissions.Scope(in.Scope)
		path, err := s.rulesPath(scope, in.SessionID, strings.TrimSpace(in.Workspace))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		projectRoot := ""
		if scope == permissions.ScopeProject {
			projectRoot = filepath.Clean(strings.TrimSpace(in.Workspace))
		}
		stored, err := permissions.AddRule(path, projectRoot, permissions.StoredRule{Rule: in.Rule, Effect: permissions.Effect(in.Effect), SourceSession: in.SessionID})
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, protocol.PermissionRule{ID: stored.ID, Rule: stored.Rule, Effect: string(stored.Effect), Scope: string(scope), CreatedAt: stored.CreatedAt.UTC().Format(time.RFC3339), SourceSession: stored.SourceSession})
	case strings.HasPrefix(rest, "/") && r.Method == http.MethodDelete:
		id, err := url.PathUnescape(strings.TrimPrefix(rest, "/"))
		if err != nil || !validID(id) {
			writeJSONError(w, http.StatusBadRequest, "invalid rule id")
			return
		}
		path, err := s.rulesPath(permissions.Scope(query.Get("scope")), sessionID, workspace)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		removed, err := permissions.RemoveRule(path, id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !removed {
			writeJSONError(w, http.StatusNotFound, "permission rule not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
