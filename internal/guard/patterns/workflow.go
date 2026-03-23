package patterns

// WorkflowRule defines a hard constraint on what a role can do.
type WorkflowRule struct {
	ID     string
	Reason string
}

// RoleBinaryAllowlist defines which binaries each role can execute.
// If a role is not listed, it can only execute binaries in the global BinaryAllowlist
// (git, go, make) with no additional restrictions.
// An empty slice means the role CANNOT execute any commands.
var RoleBinaryAllowlist = map[string][]string{
	"idea":      {},                                         // no command execution
	"architect": {},                                         // no command execution
	"breakdown": {},                                         // no command execution
	"coding":    {"git", "go", "make"},                      // code-related only
	"test":      {"git", "go", "make"},                      // test execution
	"review":    {"git", "go"},                              // read-only git + go vet
	"docs":      {},                                         // no command execution
	"deploy":    {"git", "go", "make", "docker", "kubectl"}, // deployment tools
	"notify":    {},                                         // no command execution
}

// RoleWritePathPrefixes defines which path prefixes each role can write to.
// An empty slice means the role cannot write any files.
var RoleWritePathPrefixes = map[string][]string{
	"idea":      {"memory/"},
	"architect": {"memory/", "docs/architecture/"},
	"breakdown": {}, // writes to Trello, not filesystem
	"coding":    {"internal/", "cmd/", "vendor/", "pkg/"},
	"test":      {"internal/", "cmd/", "testdata/"},
	"review":    {},                                     // reads only, comments on GitHub
	"docs":      {"docs/", "README"},                    // docs and README files
	"deploy":    {"Dockerfile", "docker-compose", "Makefile", "deploy/"},
	"notify":    {},                                     // sends messages, no file writes
}

// MaxFileWritesPerTask is the maximum number of file writes allowed per single task execution.
const MaxFileWritesPerTask = 20

// MaxCommandsPerTask is the maximum number of command executions allowed per single task execution.
const MaxCommandsPerTask = 10

// MaxInputLength defines maximum length for various input types.
var MaxInputLength = map[string]int{
	"task_title":       500,
	"task_description": 10_000,
	"memory_section":   5_000,
	"human_answer":     2_000,
	"trello_card":      10_000,
	"api_input":        10_000,
}
