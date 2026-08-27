package profiles

import (
	"fmt"
	"strings"
)

type queryField struct {
	name  string
	value string
}

func serializeURI(email string, profile Available) string {
	fields := []queryField{
		{name: "type", value: string(profile.Transport.Type)},
		{name: "encryption", value: "none"},
	}
	if profile.Flow != nil {
		fields = append(fields, queryField{name: "flow", value: *profile.Flow})
	}
	fields = append(fields, queryField{name: "security", value: string(profile.Security.Type)})

	switch profile.Transport.Type {
	case "ws", "httpupgrade":
		fields = append(fields,
			queryField{name: "path", value: profile.Transport.Path},
			queryField{name: "host", value: profile.Transport.Host},
		)
	case "grpc":
		fields = append(fields,
			queryField{name: "serviceName", value: profile.Transport.ServiceName},
			queryField{name: "mode", value: profile.Transport.Mode},
		)
		if profile.Transport.Authority != "" {
			fields = append(fields, queryField{name: "authority", value: profile.Transport.Authority})
		}
	case "xhttp":
		fields = append(fields,
			queryField{name: "path", value: profile.Transport.Path},
			queryField{name: "host", value: profile.Transport.Host},
			queryField{name: "mode", value: profile.Transport.Mode},
		)
		if profile.Transport.Extra != nil {
			fields = append(fields, queryField{name: "extra", value: string(profile.Transport.Extra)})
		}
	}

	fields = append(fields, queryField{name: "fp", value: profile.Security.Fingerprint})
	fields = append(fields, queryField{name: "sni", value: profile.Security.ServerName})
	switch profile.Security.Type {
	case "tls":
		if len(profile.Security.ALPN) != 0 {
			fields = append(fields, queryField{name: "alpn", value: strings.Join(profile.Security.ALPN, ",")})
		}
		if profile.Security.ECH != "" {
			fields = append(fields, queryField{name: "ech", value: profile.Security.ECH})
		}
		if len(profile.Security.CertificatePins) != 0 {
			fields = append(fields, queryField{name: "pcs", value: strings.Join(profile.Security.CertificatePins, ",")})
		}
		if profile.Security.VerifyName != "" {
			fields = append(fields, queryField{name: "vcn", value: profile.Security.VerifyName})
		}
	case "reality":
		fields = append(fields, queryField{name: "pbk", value: profile.Security.PublicKey})
		if profile.Security.ShortIDPresent {
			fields = append(fields, queryField{name: "sid", value: profile.Security.ShortID})
		}
		if profile.Security.PostQuantumVerify != "" {
			fields = append(fields, queryField{name: "pqv", value: profile.Security.PostQuantumVerify})
		}
		if profile.Security.SpiderX != "" {
			fields = append(fields, queryField{name: "spx", value: profile.Security.SpiderX})
		}
	}

	var query strings.Builder
	for index, field := range fields {
		if index != 0 {
			query.WriteByte('&')
		}
		query.WriteString(field.name)
		query.WriteByte('=')
		query.WriteString(encodeURIComponent(field.value))
	}
	fragment := encodeURIComponent(email + " · " + profile.Name)
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", profile.ClientID, profile.Endpoint.Host, profile.Endpoint.Port, query.String(), fragment)
}

func encodeURIComponent(value string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		if isURIComponentByte(char) {
			encoded.WriteByte(char)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[char>>4])
		encoded.WriteByte(hex[char&0x0f])
	}
	return encoded.String()
}

func isURIComponentByte(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
		strings.ContainsRune("-_.!~*'()", rune(char))
}
