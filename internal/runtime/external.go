package runtime

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// HostGateway is the name under which project containers reach the Docker host
// (host.docker.internal → host-gateway), for an external server running there.
const HostGateway = "host.docker.internal"

var (
	// externalUserRe allows the user names hosted databases hand out (app_user,
	// user@server on Azure, service.account); SQL quotes them, so no quotes of any kind.
	externalUserRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,79}$`)
	// externalDatabaseRe allows the database names found in practice; statements quote
	// them with ` or ", so neither may appear.
	externalDatabaseRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_$-]{0,63}$`)
)

// ExternalVariants are the database flavours an external server may be.
var ExternalVariants = []string{"mariadb", "mysql", "postgresql"}

func validExternalHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		if ip.IsLoopback() {
			return "", fmt.Errorf("%w: %s is the container itself; a server on the Docker host is reached as %s", validate.ErrInvalid, h, HostGateway)
		}
		return ip.String(), nil
	}
	h = validate.NormalizeHostname(h)
	if h == "localhost" {
		return "", fmt.Errorf("%w: localhost is the container itself; a server on the Docker host is reached as %s", validate.ErrInvalid, HostGateway)
	}
	if err := validate.Hostname(h); err != nil {
		return "", err
	}
	return h, nil
}

func validExternalPort(port, def int) (int, error) {
	if port == 0 {
		return def, nil
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("%w: port %d is out of range", validate.ErrInvalid, port)
	}
	return port, nil
}

func validExternalPassword(pw string) error {
	if len(pw) > 256 || strings.ContainsAny(pw, "\x00\r\n") {
		return fmt.Errorf("%w: the password must be one line of at most 256 characters", validate.ErrInvalid)
	}
	return nil
}

// NormalizeExternalDatabase checks the connection of an external database and fills in
// the flavour's default port. The server is not contacted.
func NormalizeExternalDatabase(c *DatabaseConfig, variant string) error {
	d, ok := dialects[variant]
	if !ok || !slices.Contains(ExternalVariants, variant) {
		return fmt.Errorf("%w: an external database must be %s", validate.ErrInvalid, strings.Join(ExternalVariants, ", "))
	}
	host, err := validExternalHost(c.Host)
	if err != nil {
		return err
	}
	port, err := validExternalPort(c.Port, d.Port)
	if err != nil {
		return err
	}
	if !externalUserRe.MatchString(c.Username) {
		return fmt.Errorf("%w: the user name may contain letters, digits and _ . @ - (at most 80)", validate.ErrInvalid)
	}
	if !externalDatabaseRe.MatchString(c.Database) {
		return fmt.Errorf("%w: the database name may contain letters, digits and _ $ - (at most 64)", validate.ErrInvalid)
	}
	if err := validExternalPassword(c.Password); err != nil {
		return err
	}
	c.Host, c.Port, c.RootPassword, c.HostPort = host, port, "", 0
	return nil
}

// NormalizeExternalRedis checks the address of an external Redis.
func NormalizeExternalRedis(c *ServiceConfig) error {
	host, err := validExternalHost(c.Host)
	if err != nil {
		return err
	}
	port, err := validExternalPort(c.Port, 6379)
	if err != nil {
		return err
	}
	if err := validExternalPassword(c.Password); err != nil {
		return err
	}
	c.Host, c.Port, c.HostPort = host, port, 0
	return nil
}
