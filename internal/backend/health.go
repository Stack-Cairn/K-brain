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
			SessionChanges:          2,
			SessionQueue:            1,
			ComputerSessionApproval: 1,
			MemorySwitch:            1,
			ImageModelToggle:        1,
			SessionExists:           1,
			AIReviewPerRun:          1,
			PermissionRules:         1,
			ReadOnlyAutoAllow:       1,
			PermissionModes:         append([]string(nil), permissionModes...),
			Image:                   healthImageV1{Supported: s.image != nil, Version: imageWireVersion()},
			RunSandbox:              runSandboxCapability(),
			SubagentTranscripts:     1,
			SandboxSetup:            1,
			QuestionSelectedLabels:  1,
			MCPElicitation:          1,
			HostBridge:              1,
			SSHManager:              1,
			SkillPacks:              1,
			BackgroundTool:          1,
			SessionArchive:          1,
			MemoryOrganizerSettings: 1,
			SessionExternalRuns:     1,
		},
		HostBridge: s.hostBridge.status(),
	}
}
