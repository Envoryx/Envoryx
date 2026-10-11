package project

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/tlsca"
)

// Inside the project network <slug>.<base> resolves to the proxy, which answers with a
// certificate of Envoryx's local CA. An application that calls its own address (webhooks,
// cache warmers, its own API, OAuth back-channels) only gets through when its runtime
// trusts that CA. Envoryx writes the CA and a bundle of the system CAs plus the CA into
// CATrustDir (tlsca.WriteTrust); the application and worker containers mount the directory
// read-only, and the runtimes are pointed at it by variables. The image's own system store
// stays as it is: the variables are read by the runtimes, nothing is overmounted.
const (
	// CATrustDir is the trust directory under the config directory.
	CATrustDir    = "ca-trust"
	caTrustTarget = "/etc/envoryx/ca"
)

var (
	caTrustBundle = caTrustTarget + "/" + tlsca.TrustBundleFile
	caTrustCert   = caTrustTarget + "/" + tlsca.TrustCAFile
)

// caTrustEnv are the variables that make the runtimes trust the local CA. SSL_CERT_FILE
// replaces OpenSSL's default bundle, which covers Ruby, Python's ssl module, Go and .NET;
// Python requests reads its own variable, Node adds the extra file to its built-in store
// and the curl CLI reads CURL_CA_BUNDLE. PHP reads none of them in PHP-FPM, which clears
// the environment, so the generated php.ini names the bundle (runtime.INIOptions.CAFile).
// Java keeps its own keystore and is left out.
var caTrustEnv = []string{
	"SSL_CERT_FILE=" + caTrustBundle,
	"REQUESTS_CA_BUNDLE=" + caTrustBundle,
	"CURL_CA_BUNDLE=" + caTrustBundle,
	"NODE_EXTRA_CA_CERTS=" + caTrustCert,
}

// phpCAFile is the bundle the generated php.ini points OpenSSL and curl at, "" when the
// local CA isn't in use.
func (p *Planner) phpCAFile() string {
	if !p.paths.CATrust {
		return ""
	}
	return caTrustBundle
}

// withCATrust mounts the trust directory into a container that runs project code and sets
// the variables, when the local CA is in use. A variable the project sets itself (its own
// bundle with a company CA, say) is left alone.
func (p *Planner) withCATrust(spec *docker.ContainerSpec) {
	if !p.paths.CATrust {
		return
	}
	// Clipped: the plan shares env and mount slices between containers, and an append
	// into spare capacity would change the others as well.
	spec.Env, spec.Mounts = slices.Clip(spec.Env), slices.Clip(spec.Mounts)
	spec.Mounts = append(spec.Mounts, docker.MountSpec{Type: "bind", Source: filepath.Join(p.paths.ConfigHostDir, CATrustDir), Target: caTrustTarget, ReadOnly: true})
	for _, kv := range caTrustEnv {
		key := kv[:strings.IndexByte(kv, '=')+1]
		if !hasEnvKey(spec.Env, key) {
			spec.Env = append(spec.Env, kv)
		}
	}
}

func hasEnvKey(env []string, keyEq string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, keyEq) {
			return true
		}
	}
	return false
}
