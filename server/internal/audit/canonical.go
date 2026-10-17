package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const maxSnapshotBytes = 8192

var errSnapshot = errors.New("audit: snapshot must contain only allowlisted, bounded string/bool/null fields; JSON numbers and sensitive fields are forbidden")
var codePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,63}$`)
var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var suspectValue = regexp.MustCompile(`(?i)(bearer\s|-----BEGIN[^\n]*PRIVATE KEY|(?:password|passwd|totp|secret|private[_ -]?key|access[_ -]?token)\s*[:=]|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.)`)
var suspectHex = regexp.MustCompile(`(?i)(^|\s)[0-9a-f]{64}($|\s)`)

// Explicit projection by the caller is required. Raw request, approval payload,
// credentials, signed URLs, addresses and unrestricted nested objects are not
// accepted just because their JSON is syntactically valid.
var snapshotFields = map[string]bool{
	"status": true, "version_name": true, "version_code": true,
	"min_supported_code": true, "platform": true, "published_at": true,
	"enabled": true, "action_code": true, "target_type": true,
	"target_id": true, "approval_id": true, "permission_code": true,
	"role_code": true, "error_code": true, "plan_code": true,
	"code": true, "name": true, "file_sha256": true, "cert_sha256": true,
	"decision_note": true,
}

func snapshot(raw json.RawMessage) (map[string]any, []byte, error) {
	if len(raw) > maxSnapshotBytes {
		return nil, nil, errSnapshot
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil, nil
	}
	if !validJSONStringEncoding(raw) {
		return nil, nil, errSnapshot
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, nil, errSnapshot
	}
	m := make(map[string]any)
	for d.More() {
		key, err := d.Token()
		k, ok := key.(string)
		if err != nil || !ok || !snapshotFields[k] || len(m) >= 24 {
			return nil, nil, errSnapshot
		}
		if _, duplicate := m[k]; duplicate {
			return nil, nil, errSnapshot
		}
		v, err := d.Token()
		if err != nil || !safeSnapshotValue(k, v) {
			return nil, nil, errSnapshot
		}
		m[k] = v
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return nil, nil, errSnapshot
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, nil, errSnapshot
	}
	b, err := jcs(m)
	return m, b, err
}

func safeSnapshotValue(k string, v any) bool {
	if v == nil {
		return true
	}
	if k == "enabled" {
		_, ok := v.(bool)
		return ok
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	limit := 128
	if k == "decision_note" {
		limit = 500
	}
	if !plainText(s, limit) || suspectValue.MatchString(s) {
		return false
	}
	if (k == "decision_note" || k == "name") && suspectHex.MatchString(s) {
		return false
	}
	switch k {
	case "version_code", "min_supported_code", "target_id", "approval_id":
		if !decimalPattern.MatchString(s) {
			return false
		}
		_, err := strconv.ParseInt(s, 10, 64)
		return err == nil
	case "file_sha256", "cert_sha256":
		return hashPattern.MatchString(s)
	case "action_code", "target_type", "permission_code", "role_code", "plan_code", "code":
		return codePattern.MatchString(s)
	default:
		return true
	}
}

func plainText(s string, maxRunes int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// encoding/json otherwise replaces invalid UTF-8 and lone surrogate escapes.
// Validate them before decoding so the signed data is never silently changed.
func validJSONStringEncoding(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue // json.Decoder validates all other escape sequences.
		}
		if i+4 >= len(raw) {
			return false
		}
		v, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return false
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return !inString
}

// jcs implements RFC 8785 sections 3.2.1, 3.2.2.1, 3.2.2.2 and 3.2.3
// on the audit schema's number-free subset. It deliberately rejects numbers,
// arrays and unknown Go values. No Unicode normalization or HTML escaping.
func jcs(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeJCS(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeJCS(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		if !utf8.ValidString(x) {
			return errors.New("audit: invalid Unicode")
		}
		b.WriteByte('"')
		for _, r := range x {
			switch r {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			case '\b':
				b.WriteString(`\b`)
			case '\t':
				b.WriteString(`\t`)
			case '\n':
				b.WriteString(`\n`)
			case '\f':
				b.WriteString(`\f`)
			case '\r':
				b.WriteString(`\r`)
			default:
				if r < 0x20 {
					const hex = "0123456789abcdef"
					b.WriteString(`\u00`)
					b.WriteByte(hex[byte(r)>>4])
					b.WriteByte(hex[byte(r)&15])
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	case map[string]any:
		if x == nil {
			b.WriteString("null")
			return nil
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, z := utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))
			for k := 0; k < len(a) && k < len(z); k++ {
				if a[k] != z[k] {
					return a[k] < z[k]
				}
			}
			return len(a) < len(z)
		})
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJCS(b, k); err != nil {
				return err
			}
			b.WriteByte(':')
			if err := writeJCS(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return errors.New("audit: JCS subset does not accept numbers or arrays")
	}
	return nil
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func pointerID(v *int64) any {
	if v == nil {
		return nil
	}
	return strconv.FormatInt(*v, 10)
}

func idString(v *int64) *string {
	if v == nil {
		return nil
	}
	s := strconv.FormatInt(*v, 10)
	return &s
}

func zeroHash() string { return strings.Repeat("0", 64) }
