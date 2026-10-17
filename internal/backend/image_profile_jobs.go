package backend

import (
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// JobParameters converts the workbench's unified parameters into the native
// request for the profile behind targetID. The applied values and corrections go
// back to the client, so a task card can show what was actually requested.
func (m *ImageProfilesManager) JobParameters(targetID string, params imaging.GenerationParams) (map[string]any, imaging.GenerationParams, []imaging.ParamCorrection, error) {
	profile, _, ok := imaging.FindProfileTarget(m.Source.Current(), targetID)
	if !ok {
		return nil, imaging.GenerationParams{}, nil, &imaging.ImageError{Code: imaging.CodeNotFound}
	}
	platform, ok := imaging.FindPlatform(profile.Platform)
	if !ok {
		return nil, imaging.GenerationParams{}, nil, &imaging.ImageError{Code: imaging.CodeUnsupportedOperation}
	}
	applied, fixes := imaging.NormalizeParams(platform, params)
	return imaging.NativeParameters(platform.ID, applied), applied, fixes, nil
}
