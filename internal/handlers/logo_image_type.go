// Spec: specs/010-logo-heic-iphone/spec.md
package handlers

import "bytes"

// detectImageType sniffs the real image format of data by inspecting the
// leading "magic number" bytes, ignoring any Content-Type the client
// supplied. This is the backend's defense for Feature 010: iPhone photos
// are HEIC, the client may forward an image/heic (or even a wrong) MIME,
// and the Supabase store-logos bucket only accepts jpeg/png/webp/gif.
// Sniffing here lets the handler upload with the *correct* type and
// reject HEIC with a clear 400 instead of a generic 500.
//
// Returns one of "image/jpeg", "image/png", "image/webp", "image/gif",
// "image/heic", or "" when the format is unknown / unrecognized.
func detectImageType(data []byte) string {
	// JPEG: FF D8 FF
	if len(data) >= 3 &&
		data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}

	// PNG: 89 50 4E 47 0D 0A 1A 0A
	if len(data) >= 8 &&
		bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "image/png"
	}

	// GIF: "GIF87a" / "GIF89a" — both start with "GIF8".
	if len(data) >= 6 && bytes.HasPrefix(data, []byte("GIF8")) {
		return "image/gif"
	}

	// WebP: "RIFF" at offset 0 and "WEBP" at offset 8.
	if len(data) >= 12 &&
		bytes.Equal(data[0:4], []byte("RIFF")) &&
		bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp"
	}

	// HEIC/HEIF (ISO-BMFF): "ftyp" box at offset 4, brand at offset 8.
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		switch string(data[8:12]) {
		case "heic", "heix", "hevc", "hevx", "mif1", "heif", "msf1":
			return "image/heic"
		}
	}

	return ""
}

// uploadableImageTypes are the formats the Supabase store-logos bucket
// accepts. Anything outside this set (notably HEIC) must be rejected at
// the API boundary with a 400, not forwarded to storage.
var uploadableImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

// logoFormatoNoSoportadoMsg is the Spanish, friction-free message shown
// when a merchant uploads a logo in a format the bucket cannot store
// (Constitution Art. I & V).
const logoFormatoNoSoportadoMsg = "Esa foto está en un formato que no podemos usar (HEIC). " +
	"Tómala de nuevo o elige otra en JPG o PNG."

// logoFormatoNoSoportadoCode is the stable machine-readable code clients
// can branch on without parsing the Spanish text.
const logoFormatoNoSoportadoCode = "logo_formato_no_soportado"
