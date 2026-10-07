package graphimport

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ContentHash returns the lowercase hex SHA-256 the producer stores for a file of size bytes.
// Over the size limit, rules with OversizeStamp hash "codegraph:oversize:<size>" without reading
// content; otherwise the hash covers the UTF-8 re-encoding of the WHATWG UTF-8 decoding of the
// bytes (Node's Buffer.toString('utf8')): each maximal invalid subsequence becomes U+FFFD and a
// BOM is kept.
func ContentHash(rules Rules, size int64, content io.Reader) (string, error) {
	h := sha256.New()
	if rules.OversizeStamp && size > rules.MaxSourceFileSize {
		fmt.Fprintf(h, "codegraph:oversize:%d", size)
	} else if err := decodeUTF8(h, content); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// decodeUTF8 writes the WHATWG UTF-8 decoding of src to dst, re-encoded as UTF-8.
// Valid input is copied unchanged, since a valid sequence re-encodes to its own bytes.
func decodeUTF8(dst io.Writer, src io.Reader) error {
	const replacement = "\uFFFD"
	in := bufio.NewReader(src)
	out := bufio.NewWriter(dst)
	var (
		pending      [4]byte
		seen, needed int
		lower, upper byte = 0x80, 0xBF
	)
	reset := func() { seen, needed, lower, upper = 0, 0, 0x80, 0xBF }
	for {
		b, err := in.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		for {
			if needed == 0 {
				switch {
				case b <= 0x7F:
					out.WriteByte(b)
				case b >= 0xC2 && b <= 0xDF:
					needed = 1
				case b >= 0xE0 && b <= 0xEF:
					if b == 0xE0 {
						lower = 0xA0
					}
					if b == 0xED {
						upper = 0x9F
					}
					needed = 2
				case b >= 0xF0 && b <= 0xF4:
					if b == 0xF0 {
						lower = 0x90
					}
					if b == 0xF4 {
						upper = 0x8F
					}
					needed = 3
				default:
					out.WriteString(replacement)
				}
				if needed != 0 {
					pending[0], seen = b, 1
				}
				break
			}
			if b < lower || b > upper {
				reset()
				out.WriteString(replacement)
				continue // the offending byte starts over
			}
			lower, upper = 0x80, 0xBF
			pending[seen] = b
			seen++
			if seen == needed+1 {
				out.Write(pending[:seen])
				reset()
			}
			break
		}
	}
	if needed != 0 {
		out.WriteString(replacement)
	}
	return out.Flush()
}

var (
	shopifyJSON = regexp.MustCompile(`(?i)(^|/)(templates|sections)/.+\.json$`)
	erlangApp   = regexp.MustCompile(`(?i)\.app(\.src)?$`)
)

// IsSourceFile mirrors the producer's isSourceFile: Play routes, Shopify JSON, Erlang .app files,
// else the lowercase last-dot extension in the rules' extension map or in overrides.
func IsSourceFile(rules Rules, path string, overrides map[string]string) bool {
	if path == "conf/routes" || strings.HasSuffix(path, "/conf/routes") || strings.HasSuffix(path, ".routes") {
		return true
	}
	if shopifyJSON.MatchString(path) || erlangApp.MatchString(path) {
		return true
	}
	dot := strings.LastIndex(path, ".")
	if dot < 0 {
		return false
	}
	ext := strings.ToLower(path[dot:])
	_, ok := rules.ExtensionMap[ext]
	if !ok {
		_, ok = overrides[ext]
	}
	return ok
}
