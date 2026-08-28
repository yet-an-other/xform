package journal

import (
	"context"
	"fmt"
	"strings"
)

// UnitQuerier reports systemd's own canonical identity for a unit name — the
// seam over D-Bus, faked in tests.
type UnitQuerier interface {
	CanonicalID(ctx context.Context, name string) (string, error)
}

// maxUnitNameLength is systemd's limit on a unit name.
const maxUnitNameLength = 255

// serviceSuffix is the only unit type the Panel reads logs for.
const serviceSuffix = ".service"

// ResolveXrayUnit turns the administrator's configured xray unit into the one
// canonical service identity the reader may pass to journalctl (§5.5).
//
// The configured string is checked before systemd is asked, because
// journalctl's --unit= takes glob patterns and would happily widen the read
// to every matching unit in the namespace. The answer is then checked again:
// the Panel passes systemd's identity, not the administrator's spelling, and
// an identity it cannot vouch for is refused rather than guessed at.
func ResolveXrayUnit(ctx context.Context, configured string, systemd UnitQuerier) (string, error) {
	if err := validUnitName(configured); err != nil {
		return "", fmt.Errorf("XFORM_XRAY_UNIT %q: %w", configured, err)
	}
	id, err := systemd.CanonicalID(ctx, configured)
	if err != nil {
		return "", fmt.Errorf("resolve XFORM_XRAY_UNIT %q through systemd: %w", configured, err)
	}
	if err := validUnitName(id); err != nil {
		return "", fmt.Errorf("systemd resolved %q to %q, which is not a unit the Panel can read: %w", configured, id, err)
	}
	return id, nil
}

// validUnitName accepts one concrete, canonical service name: systemd's own
// character set, a .service suffix, an instance where one is present, and
// nothing journalctl would read as a pattern or an option.
func validUnitName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return fmt.Errorf("is empty")
	case len(name) > maxUnitNameLength:
		return fmt.Errorf("is longer than systemd's %d characters", maxUnitNameLength)
	case !strings.HasSuffix(name, serviceSuffix):
		return fmt.Errorf("is not a full %s name", serviceSuffix)
	case strings.HasPrefix(name, "-"):
		// An attached --unit= argument already prevents this, but a name
		// opening with a dash has no business being a unit either.
		return fmt.Errorf("starts with a dash")
	case strings.HasSuffix(name, "@"+serviceSuffix):
		return fmt.Errorf("is a template with no instance")
	}
	for _, char := range name {
		if !validUnitChar(char) {
			return fmt.Errorf("contains %q, which is not valid in a unit name", char)
		}
	}
	return nil
}

// validUnitChar is systemd's unit-name character set. Glob metacharacters,
// path separators, whitespace, and control characters all fall outside it.
func validUnitChar(char rune) bool {
	switch {
	case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		return true
	case char == ':' || char == '-' || char == '_' || char == '.' || char == '@' || char == '\\':
		return true
	default:
		return false
	}
}
