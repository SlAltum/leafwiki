package launcher

import (
	"sort"
	"strconv"
)

// Secrets are the credentials handed to the server. They never enter the
// parameter table (FR-204).
type Secrets struct {
	JWT           string
	AdminPassword string
}

// BuildArgv assembles the server's argument vector (SRS §6.7):
//
//	argv[0] = "leafwiki"
//	--data-dir=<absolute data dir>
//	--jwt-secret, --admin-password
//	--port=<the port actually bound>
//	every other changed parameter, sorted by name
//
// Empty strings mean "not set" and are dropped, except for booleans, where
// false is a real choice.
func BuildArgv(params []Param, secrets Secrets, dataDir string, actualPort int) []string {
	argv := []string{
		"leafwiki",
		"--data-dir=" + dataDir,
		"--jwt-secret=" + secrets.JWT,
		"--admin-password=" + secrets.AdminPassword,
		"--port=" + strconv.Itoa(actualPort),
		// The launcher only ever binds 127.0.0.1 and cannot offer TLS, so the
		// server has to accept plain-HTTP auth cookies or the login page could
		// never work (NFR-6 keeps the exposure to this machine).
		"--allow-insecure=true",
	}

	names := make([]string, 0, len(params))
	for _, param := range params {
		if param.Name == "port" || !Editable(param.Name) || param.Kind == KindUnsupported {
			continue
		}
		names = append(names, param.Name)
	}
	sort.Strings(names)

	for _, name := range names {
		param := paramByName(params, name)
		switch param.Kind {
		case KindBool:
			argv = append(argv, "--"+name+"="+strconv.FormatBool(param.Value == "true"))
		default:
			if param.Value == "" {
				continue
			}
			argv = append(argv, "--"+name+"="+param.Value)
		}
	}

	return argv
}

func paramByName(params []Param, name string) Param {
	for _, param := range params {
		if param.Name == name {
			return param
		}
	}
	return Param{Name: name}
}
