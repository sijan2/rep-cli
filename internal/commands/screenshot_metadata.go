package commands

type locateScreenshot struct {
	Path           string  `json:"path"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Bytes          int     `json:"bytes"`
	SHA256         string  `json:"sha256"`
	PixelsPerCSSPx float64 `json:"pixels_per_css_px"`
	Scope          string  `json:"scope"`
}
