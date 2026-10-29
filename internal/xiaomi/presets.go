package xiaomi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func parseCameraPresets(value json.RawMessage) ([]xiaomiPreset, error) {
	var encoded string
	if err := json.Unmarshal(value, &encoded); err != nil {
		return nil, fmt.Errorf("decode preset property: %w", err)
	}
	var areas []struct {
		Index    *int   `json:"idx"`
		Location *int   `json:"location"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal([]byte(encoded), &areas); err != nil {
		// Do not echo the cloud payload, which can contain private device data.
		return nil, errors.New("decode preset list: invalid JSON")
	}
	presets := make([]xiaomiPreset, 0, len(areas))
	seen := make(map[int]bool)
	for _, area := range areas {
		location := area.Location
		if location == nil {
			location = area.Index
		}
		if location == nil || *location < 0 || *location > 64 || seen[*location] {
			return nil, errors.New("preset list has a missing, invalid or duplicate location")
		}
		seen[*location] = true
		presets = append(presets, xiaomiPreset{
			Token: strconv.Itoa(*location), Name: presetName(area.Name),
		})
	}
	return presets, nil
}

func presetName(name string) string {
	// Plain names such as "Home" can also parse as Base64. Only use decoded
	// names when they are printable UTF-8; retain the original otherwise.
	if decoded, err := base64.StdEncoding.DecodeString(name); err == nil &&
		len(decoded) > 0 && utf8.Valid(decoded) &&
		strings.IndexFunc(string(decoded), func(r rune) bool { return !unicode.IsPrint(r) }) < 0 {
		return string(decoded)
	}
	return name
}

func hasPreset(presets []xiaomiPreset, token string) bool {
	for _, preset := range presets {
		if preset.Token == token {
			return true
		}
	}
	return false
}
