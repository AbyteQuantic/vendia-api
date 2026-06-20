// Spec: specs/070-galeria-multimedia-producto/spec.md
package services

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// youtubeIDRe valida un ID de YouTube (11 chars del alfabeto base64-url).
var youtubeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ParseYouTubeID extrae y valida el videoId de un link de YouTube. Acepta las
// formas comunes (watch?v=, youtu.be/, shorts/, embed/). Devuelve error si el
// link no es de YouTube o el id no tiene forma válida. NO hace fetch externo
// (lección Turnstile/Honeypot: nada de llamadas en el camino crítico).
func ParseYouTubeID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("link de YouTube vacío")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("link inválido")
	}
	host := strings.ToLower(strings.TrimPrefix(u.Host, "www."))
	switch host {
	case "youtube.com", "m.youtube.com", "music.youtube.com":
		// watch?v=<id>
		if v := u.Query().Get("v"); v != "" {
			if youtubeIDRe.MatchString(v) {
				return v, nil
			}
			return "", errors.New("id de YouTube inválido")
		}
		// /shorts/<id> o /embed/<id>
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed") {
			if youtubeIDRe.MatchString(parts[1]) {
				return parts[1], nil
			}
		}
		return "", errors.New("no encontramos el video en el link")
	case "youtu.be":
		id := strings.Trim(u.Path, "/")
		if youtubeIDRe.MatchString(id) {
			return id, nil
		}
		return "", errors.New("id de YouTube inválido")
	default:
		return "", errors.New("el link debe ser de YouTube")
	}
}

// YouTubeThumbnail devuelve la miniatura pública de un video (sin fetch).
func YouTubeThumbnail(id string) string {
	return fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", id)
}

// YouTubeCanonicalURL normaliza a la URL watch estándar.
func YouTubeCanonicalURL(id string) string {
	return "https://www.youtube.com/watch?v=" + id
}

// ErrNoDuration indica que no se pudo determinar la duración del MP4 (el peso
// queda como guardrail fail-closed).
var ErrNoDuration = errors.New("no se pudo determinar la duración del video")

// Mp4DurationSeconds intenta leer la duración (segundos) de un MP4/MOV leyendo
// el átomo `mvhd` dentro de `moov`. Recorrido superficial de boxes ISO-BMFF; no
// decodifica el video. Devuelve ErrNoDuration si no encuentra un mvhd usable —
// el caller usa el tope de tamaño como segunda barrera.
func Mp4DurationSeconds(data []byte) (int, error) {
	moov := findBox(data, "moov")
	if moov == nil {
		return 0, ErrNoDuration
	}
	mvhd := findBox(moov, "mvhd")
	if mvhd == nil || len(mvhd) < 1 {
		return 0, ErrNoDuration
	}
	version := mvhd[0]
	// mvhd: 1 byte version + 3 flags, luego created/modified, timescale, duration.
	if version == 1 {
		// 4(ver+flags)+8(created)+8(modified)+4(timescale)+8(duration)
		if len(mvhd) < 32 {
			return 0, ErrNoDuration
		}
		timescale := binary.BigEndian.Uint32(mvhd[20:24])
		duration := binary.BigEndian.Uint64(mvhd[24:32])
		if timescale == 0 {
			return 0, ErrNoDuration
		}
		return int(duration / uint64(timescale)), nil
	}
	// version 0: 4+4(created)+4(modified)+4(timescale)+4(duration)
	if len(mvhd) < 20 {
		return 0, ErrNoDuration
	}
	timescale := binary.BigEndian.Uint32(mvhd[12:16])
	duration := binary.BigEndian.Uint32(mvhd[16:20])
	if timescale == 0 {
		return 0, ErrNoDuration
	}
	return int(duration / timescale), nil
}

// findBox recorre los boxes ISO-BMFF de `data` y devuelve el CONTENIDO (sin el
// header de 8 bytes) del primer box con el `boxType` dado, o nil.
func findBox(data []byte, boxType string) []byte {
	i := 0
	for i+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[i : i+4]))
		typ := string(data[i+4 : i+8])
		if size < 8 {
			// size 0 = hasta el final; size 1 = 64-bit (no soportado aquí).
			if size == 0 {
				if typ == boxType {
					return data[i+8:]
				}
			}
			return nil
		}
		if i+size > len(data) {
			size = len(data) - i // tolerante a archivos truncados
		}
		if typ == boxType {
			return data[i+8 : i+size]
		}
		i += size
	}
	return nil
}
