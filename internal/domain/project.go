package domain

// ProjectConfig holds all human-provided project requirements.
// Every field must have an explicit human answer — agents NEVER fill defaults.
type ProjectConfig struct {
	// Repository
	RepoURL       string `json:"repo_url"`
	Branch        string `json:"branch"`         // default "main" only if human confirms
	WorkDir       string `json:"work_dir"`        // clone target: workspace/<project>
	RepoStructure string `json:"repo_structure"`  // "mono" | "multi"

	// Tech stack
	FrontendFramework string `json:"frontend_framework"` // "flutter" | "react_native" | "web" | ...
	BackendLanguage   string `json:"backend_language"`   // "go" | "node" | "python" | ...
	Database          string `json:"database"`           // "postgresql" | "mysql" | "firebase" | ...

	// Features
	AuthMethod       string   `json:"auth_method"`       // "google_oauth" | "email_password" | "phone_otp" | ...
	TargetPlatforms  []string `json:"target_platforms"`   // ["ios", "android", "web"]
	Integrations     []string `json:"integrations"`       // ["stripe", "google_maps", "firebase_push"]
}

// RequiredFields returns the list of field names that must be filled
// before the project is considered ready for pipeline execution.
var RequiredFields = []string{
	"frontend_framework",
	"backend_language",
	"repo_url",
	"repo_structure",
	"database",
	"auth_method",
	"target_platforms",
	"integrations",
}

// IdeaClarifyResult is the JSON structure returned by Opus during idea clarification.
type IdeaClarifyResult struct {
	Ready     bool     `json:"ready"`
	Questions []string `json:"questions,omitempty"` // non-empty when ready=false
	Concept   string   `json:"concept,omitempty"`   // filled when ready=true

	// Parsed answers from human — accumulated across rounds.
	Config ProjectConfig `json:"config,omitempty"`
}

// MidClarifyResult is the JSON structure returned by any agent
// when it needs more information during execution.
type MidClarifyResult struct {
	NeedsClarification bool     `json:"needs_clarification"`
	Questions          []string `json:"questions,omitempty"`
	PartialOutput      string   `json:"partial_output,omitempty"` // work done so far
	Context            string   `json:"context,omitempty"`        // what the agent is doing
}
