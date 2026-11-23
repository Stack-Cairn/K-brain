package backend

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// healthCapabilities keeps every historical capability at its registered
// representation (internal/protocol/capabilities.json). Frozen integers stay 1
// forever; enhancements get new names so old `!== 1` checks keep working.
type healthCapabilities struct {
	SessionChanges          int                `json:"session_changes"`
	SessionQueue            int                `json:"session_queue"`
	ComputerSessionApproval int                `json:"computer_session_approval"`
	MemorySwitch            int                `json:"memory_switch"`
	ImageModelToggle        int                `json:"image_model_toggle"`
	SessionExists           int                `json:"session_exists"`
	AIReviewPerRun          int                `json:"ai_review_per_run"`
	PermissionRules         int                `json:"permission_rules"`
	ReadOnlyAutoAllow       int                `json:"readonly_auto_allow"`
	PermissionModes         []string           `json:"permission_modes"`
	Image                   healthImageV1      `json:"image"`
	RunSandbox              healthRunSandboxV1 `json:"run_sandbox"`
	SubagentTranscripts     int                `json:"subagent_transcripts"`
	SandboxSetup            int                `json:"sandbox_setup"`
	// QuestionSelectedLabels is 1 where question answers accept
	// selected_labels and question history carries question_answers.
	QuestionSelectedLabels int `json:"question_selected_labels"`
	// MCPElicitation is 1 where runs accept options.mcp_elicitation and serve
	// MCP servers' elicitation forms through elicitation.requested and
	// POST /v1/sessions/{id}/elicitations/{elicitation_id}.
	MCPElicitation int `json:"mcp_elicitation"`
	// HostBridge is 1 where the desktop can register a tool bridge with
	// PUT/DELETE /v1/host-bridge and health reports host_bridge.
	HostBridge int `json:"host_bridge"`
	// SSHManager is 1 where the native SSHManager tool runs SSH/SFTP for the
	// hosts stored with GET/PUT /v1/settings/ssh.
	SSHManager int `json:"ssh_manager"`
	// SkillPacks is 1 where skills nested in skill packs are discovered:
	// /v1/skills items carry pack and shadowed, the response lists shadowed
	// copies, and /v1/skills/settings accepts packs.
	SkillPacks int `json:"skill_packs"`
	// BackgroundTool is 1 where POST
	// /v1/sessions/{id}/runs/{run_id}/tools/{tool_call_id}/background moves a
	// running foreground shell command to the background.
	BackgroundTool int `json:"background_tool"`
	// SessionArchive is 1 where the session list, snapshots, changes and
	// workdirs accept archived=exclude|include|only and every session summary
	// carries archived.
	SessionArchive int `json:"session_archive"`
	// MemoryOrganizerSettings is 1 where /v1/settings reads and writes
	// memoryOrganizer {enabled, intervalHours} and saving it reschedules the
	// running organizer.
	MemoryOrganizerSettings int `json:"memory_organizer_settings"`
	// SessionExternalRuns is 1 where processes sharing the data root (the kn
	// terminal UI, kn run, this backend) hold a per-session run lease: session
	// summaries carry external_run while another process runs a turn, runs and
	// history edits for such a session are refused with 409, and the session
	// list notices transcript writes from those processes within about a second.
	SessionExternalRuns int `json:"session_external_runs"`
	// WebSearchFallback is 1 where a run with options.search "enabled" on a
	// provider without native search (Chat Completions, Gemini relays) gets the
	// web_search tool backed by the configured search service, provider
	// settings carry nativeWebSearchSupported, and /v1/settings reads and
	// writes webSearch {provider, apiKey, baseUrl, maxResults} with available
	// and reason.
	WebSearchFallback int `json:"web_search_fallback"`
	// UsageStats is 1 where GET /v1/usage/stats?days=N&tz=IANA returns token
	// usage across every session of every workspace, by day and by model,
	// with totals, streaks and costs where the model catalog prices them.
	UsageStats int `json:"usage_stats"`
	// FolderTrust is 1 where GET/POST/DELETE /v1/trust read, grant and
	// revoke project folder trust, which gates project prompt templates and
	// project slash commands.
	FolderTrust int `json:"folder_trust"`
	// Plugins is 1 where sessions load K-brain plugins (user plugins always,
	// a workspace's own plugins only while its folder is trusted) and
	// /v1/plugins lists, enables, disables, installs and removes them, with
	// since/wait long-polling for changes.
	Plugins int `json:"plugins"`
	// PluginUI is 1 where plugin.json may declare ui.panels, GET /v1/plugins
	// lists them under contributes.ui with a url, /v1/plugin-assets serves
	// their files read-only for sandboxed frames, and POST
	// /v1/plugins/{id}/tools/call runs a plugin's own tools for its panel.
	PluginUI int `json:"plugin_ui"`
	// MCPApps is 1 where tool.result carries ui_resource for MCP tools whose
	// _meta declares a ui:// view, GET /v1/mcp/tools marks them with
	// uiResourceUri, and GET /v1/sessions/{id}/mcp-ui?server=&uri= returns the
	// view's HTML (resources/read) with sandbox headers.
	MCPApps int `json:"mcp_apps"`
	// HTMLPreviews is 1 where runs accept options.html_previews: the model is
	// told about "html preview" fenced blocks, which (and .html files written
	// by the Write tool) are published as preview.created and served by
	// GET /v1/sessions/{id}/previews/{previewId} with sandbox headers.
	HTMLPreviews int `json:"html_previews"`
	// MCPAppsStructuredContent is 1 where an MCP App's ui_resource carries
	// tool_name and the server's raw CallToolResult (result inline when small,
	// result_url for GET /v1/sessions/{id}/mcp-ui/results/{ref}), history and
	// subagent tool_calls[] keep ui_resource, and POST /v1/mcp/tools/call
	// returns result.
	MCPAppsStructuredContent int `json:"mcp_apps_structured_content"`
	// MCPToolVisibility is 1 where _meta.ui.visibility is honored: app-only
	// tools are not offered to the model, GET /v1/mcp/tools takes
	// audience=model|app and lists visibility, and POST /v1/mcp/tools/call
	// takes caller=model|app.
	MCPToolVisibility int `json:"mcp_tool_visibility"`
	// MCPToolCallApproval is 1 where POST /v1/mcp/tools/call answers a tool
	// under an "ask" policy with status approval_required and an approval id,
	// and runs it when the request is repeated with approval_id.
	MCPToolCallApproval int `json:"mcp_tool_call_approval"`
	// MCPToolServerIDs is 1 where GET /v1/mcp/tools reports serverId as the
	// configured server id, with serverKey (the name key), serverToolName and
	// serverLabel (the server's optional display name).
	MCPToolServerIDs int `json:"mcp_tool_server_ids"`
	// PluginToolNames is 1 where plugin tool names stay unambiguous for ids
	// with "_" (plugins.ToolKey), GET /v1/plugins lists contributes.toolDetails
	// and POST /v1/plugins/{id}/tools/call takes either tool name.
	PluginToolNames int `json:"plugin_tool_names"`
	// PluginUIErrors is 1 where contributes.ui[] reports errorCode/error for
	// panels that cannot load, and failed plugin assets carry
	// X-K-Brain-Asset-Error (pages also post host.panelLoadError).
	PluginUIErrors int `json:"plugin_ui_errors"`
	// PluginManifestUnknownFields is 1 where plugin.json fields this backend
	// does not know are ignored instead of failing the load.
	PluginManifestUnknownFields int `json:"plugin_manifest_unknown_fields"`
	// FilesystemMentionList is 1 where POST /v1/fs accepts action
	// "mention_list": workspace files and directories for @ mentions, without
	// ignored paths, ranked by query and capped with truncated.
	FilesystemMentionList int `json:"filesystem_mention_list"`
}

// healthRunSandboxV1 describes per-run sandboxing (options.sandbox). Clients
// offer sandbox modes only when supported is true, and the offline variant only
// when network_control is true as well.
type healthRunSandboxV1 struct {
	Supported      bool     `json:"supported"`
	Version        string   `json:"version"`
	Mechanism      string   `json:"mechanism"`
	Platform       string   `json:"platform"`
	NetworkControl bool     `json:"network_control"`
	ReadMasking    bool     `json:"read_masking"`
	PathProtection bool     `json:"path_protection"`
	Modes          []string `json:"modes"`
	Reason         string   `json:"reason,omitempty"`
	// SetupAvailable is true where POST /v1/sandbox/setup can install the
	// elevated sandbox level (Windows), SetupHint what to tell the user.
	SetupAvailable bool   `json:"setup_available"`
	SetupHint      string `json:"setup_hint"`
}

type healthImageV1 struct {
	Supported bool   `json:"supported"`
	Version   string `json:"version"`
}

type healthResponse struct {
	Version            string                `json:"version"`
	Status             string                `json:"status"`
	Protocol           protocol.ProtocolInfo `json:"protocol"`
	Build              protocol.Build        `json:"build"`
	StartupContract    int                   `json:"startupContract"`
	InstanceID         string                `json:"instanceId"`
	CapabilityRevision uint64                `json:"capabilityRevision"`
	Capabilities       healthCapabilities    `json:"capabilities"`
	// HostBridge says whether a desktop tool bridge is registered and which tools
	// it provides; never its URL or token.
	HostBridge hostBridgeStatus `json:"host_bridge"`
}

// capabilityRevision changes only when runtime availability changes. Image
// support is fixed when the server is constructed; the sandbox setup
// (POST /v1/sandbox/setup) can change run_sandbox while running and bumps it.
func capabilityRevision() uint64 { return 1 + sandboxSetupChanges.Load() }

var permissionModes = []string{"normal", "plan", "auto_edit", "full_access", "always", "auto_review"}

// instanceIdentity is created lazily so zero-value test servers still report a
// stable non-empty id for their lifetime.
type instanceIdentity struct {
	once sync.Once
	id   string
}

func (i *instanceIdentity) get() string {
	i.once.Do(func() { i.id = newInstanceID() })
	return i.id
}

// newInstanceID returns an RFC 4122 version 4 UUID. It is regenerated on every
// process start and must never be derived from the listen address.
func newInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("backend: instance id entropy unavailable: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Server) health() healthResponse {
	build := s.build
	if build.Version == "" {
		build = protocol.NewBuild("", build.Revision)
	}
	return healthResponse{
		Version:            protocol.Version,
		Status:             "ok",
		Protocol:           protocol.CurrentProtocol(),
		Build:              build,
		StartupContract:    protocol.StartupContract,
		InstanceID:         s.instance.get(),
		CapabilityRevision: capabilityRevision(),
		Capabilities: healthCapabilities{
			SessionChanges:              2,
			SessionQueue:                1,
			ComputerSessionApproval:     1,
			MemorySwitch:                1,
			ImageModelToggle:            1,
			SessionExists:               1,
			AIReviewPerRun:              1,
			PermissionRules:             1,
			ReadOnlyAutoAllow:           1,
			PermissionModes:             append([]string(nil), permissionModes...),
			Image:                       healthImageV1{Supported: s.image != nil, Version: imageWireVersion()},
			RunSandbox:                  runSandboxCapability(),
			SubagentTranscripts:         1,
			SandboxSetup:                1,
			QuestionSelectedLabels:      1,
			MCPElicitation:              1,
			HostBridge:                  1,
			SSHManager:                  1,
			SkillPacks:                  1,
			BackgroundTool:              1,
			SessionArchive:              1,
			MemoryOrganizerSettings:     1,
			SessionExternalRuns:         1,
			WebSearchFallback:           1,
			UsageStats:                  1,
			FolderTrust:                 1,
			Plugins:                     1,
			PluginUI:                    1,
			MCPApps:                     1,
			HTMLPreviews:                1,
			MCPAppsStructuredContent:    1,
			MCPToolVisibility:           1,
			MCPToolCallApproval:         1,
			MCPToolServerIDs:            1,
			PluginToolNames:             1,
			PluginUIErrors:              1,
			PluginManifestUnknownFields: 1,
			FilesystemMentionList:       1,
		},
		HostBridge: s.hostBridge.status(),
	}
}
