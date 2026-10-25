package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type videoProfile struct {
	width  int
	height int
	fps    int
	codec  string
}

func (p videoProfile) validate() error {
	if p.width < 1 || p.height < 1 || p.fps < 1 || p.fps > 120 || (p.codec != "H264" && p.codec != "H265") {
		return errors.New("width and height must be positive, fps must be 1..120, codec must be H264 or H265")
	}
	return nil
}

type cameraProfileFlag []string

func (f *cameraProfileFlag) String() string { return strings.Join(*f, ";") }

func (f *cameraProfileFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

// resolveCameraProfiles copies the global defaults before applying each camera's
// explicit fields. All validation happens before any listeners are started.
func resolveCameraProfiles(cameras, overrides []string, defaults videoProfile) (map[string]videoProfile, error) {
	defaults.codec = strings.ToUpper(strings.TrimSpace(defaults.codec))
	if err := defaults.validate(); err != nil {
		return nil, fmt.Errorf("invalid default ONVIF profile: %w", err)
	}
	profiles := make(map[string]videoProfile, len(cameras))
	for _, camera := range cameras {
		name, _, _, err := parseCameraMapping(camera)
		if err != nil {
			return nil, fmt.Errorf("invalid camera mapping: %w", err)
		}
		profiles[name] = defaults
	}
	seen := make(map[string]bool, len(overrides))
	for _, override := range overrides {
		name, fields, ok := strings.Cut(override, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || fields == "" {
			return nil, errors.New("camera profile must use NAME=width=N,height=N,fps=N,codec=H264|H265 with at least one field")
		}
		profile, ok := profiles[name]
		if !ok {
			return nil, fmt.Errorf("camera profile references unknown Xiaomi camera %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate camera profile for %q", name)
		}
		seen[name] = true
		keys := make(map[string]bool)
		for _, field := range strings.Split(fields, ",") {
			key, value, ok := strings.Cut(field, "=")
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			if !ok || value == "" {
				return nil, errors.New("camera profile fields must use KEY=VALUE")
			}
			if keys[key] {
				return nil, fmt.Errorf("duplicate camera profile field %q", key)
			}
			keys[key] = true
			if key == "codec" {
				profile.codec = strings.ToUpper(value)
				continue
			}
			var target *int
			switch key {
			case "width":
				target = &profile.width
			case "height":
				target = &profile.height
			case "fps":
				target = &profile.fps
			default:
				return nil, fmt.Errorf("unknown camera profile field %q", key)
			}
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("camera profile field %q must be an integer", key)
			}
			*target = number
		}
		if err := profile.validate(); err != nil {
			return nil, fmt.Errorf("invalid camera profile for %q: %w", name, err)
		}
		profiles[name] = profile
	}
	return profiles, nil
}
