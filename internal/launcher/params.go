package launcher

import (
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"
)

// Kind is how a parameter is edited in the table (FR-503).
type Kind int

const (
	KindString Kind = iota
	KindBool
	KindInt
	KindDuration
	// KindUnsupported is every other flag type: shown read-only, never saved.
	KindUnsupported
)

// Param is one row of the advanced page (SRS §6.7).
type Param struct {
	Group   string
	Name    string
	Kind    Kind
	Default string
	Value   string
	Env     []string
	Usage   string
	Changed bool
}

// reserved are the flags the launcher owns or must not persist (FR-511): the
// data directory comes from the executable's location, the credentials from the
// files it manages, and host/unix-socket/log-format from the window's own
// assumptions.
var reserved = map[string]bool{
	"data-dir":       true,
	"public-access":  true,
	"disable-auth":   true,
	"jwt-secret":     true,
	"admin-password": true,
	"host":           true,
	"unix-socket":    true,
	"log-format":     true,
	// allow-insecure is decided by the launcher: loopback-only, plain HTTP.
	"allow-insecure": true,
}

// VisibleByDefault is FR-505's short list: everything else waits behind
// "show advanced".
var VisibleByDefault = []string{
	"port",
	"custom-stylesheet",
	"vectors",
	"vectors-model-dir",
	"snapshot-interval",
	"snapshot-retention",
	"default-language",
}

// Collect turns the CLI's own declarations into table rows, so the page cannot
// drift from --help (FR-501/502).
func Collect(flags []cli.Flag) []Param {
	params := make([]Param, 0, len(flags))

	for _, flag := range flags {
		name := flag.Names()[0]
		param := Param{Name: name, Kind: kindOf(flag)}

		if categorizable, ok := flag.(cli.CategorizableFlag); ok {
			param.Group = categorizable.GetCategory()
		}
		param.Default = defaultOf(flag)
		if doc, ok := flag.(cli.DocGenerationFlag); ok {
			param.Env = doc.GetEnvVars()
			param.Usage = doc.GetUsage()
		}
		param.Value = param.Default
		params = append(params, param)
	}

	// Declaration order, not alphabetical: the page lists the parameters the way
	// the CLI declares them, so the sections follow --help.
	return params
}

// defaultOf reads the declared default off the flag's own typed field.
// DocGenerationFlag.GetValue is not used for the editable types: it renders a
// display form (a string default comes back quoted), which would both show
// wrongly and never compare equal to the current value.
func defaultOf(flag cli.Flag) string {
	switch f := flag.(type) {
	case *cli.StringFlag:
		return f.Value
	case *cli.BoolFlag:
		return strconv.FormatBool(f.Value)
	case *cli.IntFlag:
		return strconv.Itoa(f.Value)
	case *cli.DurationFlag:
		return f.Value.String()
	}
	if doc, ok := flag.(cli.DocGenerationFlag); ok {
		return doc.GetValue()
	}
	return ""
}

func kindOf(flag cli.Flag) Kind {
	switch flag.(type) {
	case *cli.StringFlag:
		return KindString
	case *cli.BoolFlag:
		return KindBool
	case *cli.IntFlag:
		return KindInt
	case *cli.DurationFlag:
		return KindDuration
	default:
		return KindUnsupported
	}
}

// Editable reports whether a parameter may be changed and persisted
// (FR-501/511).
func Editable(name string) bool {
	return !reserved[name]
}

// Keep is Editable in the shape Config.Load and Config.Save expect.
func Keep(name string) bool {
	return Editable(name)
}

// Visible reports whether a row shows before "show advanced" is ticked
// (FR-505): changed rows and the short list, never read-only ones.
func Visible(param Param) bool {
	if param.Changed {
		return true
	}
	for _, name := range VisibleByDefault {
		if param.Name == name {
			return true
		}
	}
	return false
}

// Filter applies the search box and the "only changed" switch (FR-504). The
// search matches the name and the description.
func Filter(params []Param, search string, onlyChanged bool) []Param {
	needle := strings.ToLower(strings.TrimSpace(search))

	out := make([]Param, 0, len(params))
	for _, param := range params {
		if onlyChanged && !param.Changed {
			continue
		}
		if needle != "" &&
			!strings.Contains(strings.ToLower(param.Name), needle) &&
			!strings.Contains(strings.ToLower(param.Usage), needle) {
			continue
		}
		out = append(out, param)
	}
	return out
}

// ChangedParams returns the rows whose value differs from the default, keyed by
// flag name: that is exactly what launcher.json stores (FR-509).
func ChangedParams(params []Param) map[string]string {
	out := map[string]string{}
	for _, param := range params {
		if param.Changed && Editable(param.Name) {
			out[param.Name] = param.Value
		}
	}
	return out
}
