package gateway

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"mycdn/internal/config"
	"mycdn/internal/rules"
)

func normalizeHost(authority string) (string, error) {
	if authority == "" || strings.ContainsAny(authority, " /\\\t\r\n@?#") {
		return "", fmt.Errorf("invalid Host")
	}
	host := authority
	if strings.HasPrefix(authority, "[") {
		if strings.HasSuffix(authority, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(authority, "["), "]")
		} else {
			h, p, err := net.SplitHostPort(authority)
			if err != nil || !validPort(p) {
				return "", fmt.Errorf("invalid Host")
			}
			host = h
		}
	} else if strings.Contains(authority, ":") {
		h, p, err := net.SplitHostPort(authority)
		if err != nil || !validPort(p) {
			return "", fmt.Errorf("invalid Host")
		}
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return "", fmt.Errorf("invalid Host")
		}
		return ip.String(), nil
	}
	if host == "" || len(host) > 253 {
		return "", fmt.Errorf("invalid Host")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid Host")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("invalid Host")
			}
		}
	}
	return host, nil
}

func validPort(s string) bool { n, err := strconv.Atoi(s); return err == nil && n > 0 && n <= 65535 }

func peerAddress(remote string) netip.Addr {
	if ip, err := netip.ParseAddr(remote); err == nil {
		return ip.Unmap()
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return netip.Addr{}
	}
	ip, _ := netip.ParseAddr(host)
	return ip.Unmap()
}

func identity(snapshot *config.Snapshot, settings rules.IdentitySettings, r *http.Request) (string, string) {
	peer := peerAddress(r.RemoteAddr)
	client := peer
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if !peer.IsValid() {
		return "", scheme
	}
	if !snapshot.IsTrusted(peer) {
		return peer.String(), scheme
	}
	sources := settings.ClientIPHeaders
	if sources == nil {
		switch settings.ClientIPSource {
		case "trusted_x_forwarded_for":
			sources = []rules.ClientIPHeader{{Name: "X-Forwarded-For", Format: "xff"}}
		case "trusted_forwarded":
			sources = []rules.ClientIPHeader{{Name: "Forwarded", Format: "forwarded"}}
		}
	}
	// Trust is explicitly configured per header. A single-valued source is safe
	// only when the immediate trusted proxy overwrites that header; appended
	// multi-hop identity must use xff/forwarded instead.
	for _, source := range sources {
		var chain []netip.Addr
		values := r.Header.Values(source.Name)
		switch source.Format {
		case "single":
			if len(values) == 1 && len(values[0]) <= 8192 {
				ip, err := netip.ParseAddr(strings.TrimSpace(values[0]))
				if err == nil && ip.Zone() == "" {
					chain = []netip.Addr{ip.Unmap()}
				}
			}
		case "xff":
			chain = parseXFF(values)
		case "forwarded":
			chain, _ = parseForwarded(values)
		}
		if len(chain) == 0 {
			continue
		}
		// Walk from our peer toward the client. Stop at the first untrusted hop;
		// attacker-supplied leftmost addresses never override that boundary.
		for i := len(chain) - 1; i >= 0; i-- {
			if !snapshot.IsTrusted(client) {
				break
			}
			client = chain[i]
		}
		break
	}
	switch settings.SchemeSource {
	case "trusted_x_forwarded_proto":
		values := r.Header.Values("X-Forwarded-Proto")
		// Do not guess which member of an ambiguous scheme chain is trustworthy.
		if len(values) == 1 {
			v := strings.ToLower(strings.TrimSpace(values[0]))
			if v == "http" || v == "https" {
				scheme = v
			}
		}
	case "trusted_forwarded":
		_, proto := parseForwarded(r.Header.Values("Forwarded"))
		if proto != "" {
			scheme = proto
		}
	}
	return client.String(), scheme
}

func parseXFF(lines []string) []netip.Addr {
	if len(lines) > 16 {
		return nil
	}
	var chain []netip.Addr
	for _, line := range lines {
		if len(line) > 8192 {
			return nil
		}
		for _, part := range strings.Split(line, ",") {
			ip, err := netip.ParseAddr(strings.TrimSpace(part))
			if err != nil || ip.Zone() != "" || len(chain) >= 64 {
				return nil
			}
			chain = append(chain, ip.Unmap())
		}
	}
	return chain
}

// Forwarded syntax is parsed with quote awareness; malformed or obfuscated
// identifiers invalidate the whole chain rather than partially trusting it.
func parseForwarded(lines []string) ([]netip.Addr, string) {
	if len(lines) > 16 {
		return nil, ""
	}
	var chain []netip.Addr
	lastProto := ""
	for _, line := range lines {
		if len(line) > 8192 {
			return nil, ""
		}
		elements, ok := splitQuoted(line, ',')
		if !ok {
			return nil, ""
		}
		for _, element := range elements {
			parts, ok := splitQuoted(element, ';')
			if !ok {
				return nil, ""
			}
			var ip netip.Addr
			proto := ""
			seen := map[string]bool{}
			for _, part := range parts {
				key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
				key = strings.ToLower(key)
				if !ok || seen[key] {
					return nil, ""
				}
				seen[key] = true
				if strings.HasPrefix(value, "\"") {
					decoded, valid := unquoteForwarded(value)
					if !valid {
						return nil, ""
					}
					value = decoded
				} else if strings.ContainsAny(value, " \t\"") {
					return nil, ""
				}
				switch key {
				case "for":
					ip, _ = netip.ParseAddr(value)
					if !ip.IsValid() {
						if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
							ip, _ = netip.ParseAddr(value[1 : len(value)-1])
						} else {
							host, port, err := net.SplitHostPort(value)
							if err != nil || !validPort(port) {
								return nil, ""
							}
							ip, _ = netip.ParseAddr(host)
						}
					}
					if !ip.IsValid() || ip.Zone() != "" {
						return nil, ""
					}
					ip = ip.Unmap()
				case "proto":
					proto = strings.ToLower(value)
					if proto != "http" && proto != "https" {
						return nil, ""
					}
				}
			}
			if !ip.IsValid() || len(chain) >= 64 {
				return nil, ""
			}
			chain = append(chain, ip)
			lastProto = proto
		}
	}
	return chain, lastProto
}

func unquoteForwarded(s string) (string, bool) {
	if len(s) < 2 || s[len(s)-1] != '"' {
		return "", false
	}
	var out strings.Builder
	for i := 1; i < len(s)-1; i++ {
		c := s[i]
		if c == '\\' {
			i++
			if i >= len(s)-1 {
				return "", false
			}
			c = s[i]
		} else if c == '"' {
			return "", false
		}
		if c < 32 && c != '\t' || c == 127 {
			return "", false
		}
		out.WriteByte(c)
	}
	return out.String(), true
}

func splitQuoted(s string, separator byte) ([]string, bool) {
	var parts []string
	start := 0
	quoted, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < ' ' && c != '\t' {
			return nil, false
		}
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if c == separator && !quoted {
			if strings.TrimSpace(s[start:i]) == "" {
				return nil, false
			}
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	if quoted || escaped || strings.TrimSpace(s[start:]) == "" {
		return nil, false
	}
	return append(parts, s[start:]), true
}
