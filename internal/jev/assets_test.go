package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x += 7 {
		img.Set(x, 0, color.RGBA{R: 255, A: 255})
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestDecodeImageEnforcesDocumentedLimits(t *testing.T) {
	ok, err := DecodeImage(testPNG(t, 400, 300))
	if err != nil || ok.Format != "png" || ok.Width != 400 || ok.Height != 300 {
		t.Fatalf("valid PNG rejected: %+v %v", ok, err)
	}
	for name, data := range map[string][]byte{
		"too small":    testPNG(t, 100, 100),
		"too wide":     testPNG(t, 1600, 300),
		"too tall":     testPNG(t, 300, 1600),
		"not an image": []byte("hello"),
		"empty":        nil,
	} {
		if _, err := DecodeImage(data); err == nil || CodeOf(err) != CodeInvalidRequest {
			t.Fatalf("%s: expected invalid request, got %v", name, err)
		}
	}
	animated := testPNG(t, 400, 300)
	// Insert an acTL chunk right after IHDR (8 signature + 25 IHDR bytes).
	chunk := append([]byte{0, 0, 0, 8, 'a', 'c', 'T', 'L'}, make([]byte, 12)...)
	animated = append(animated[:33], append(chunk, animated[33:]...)...)
	if _, err := DecodeImage(animated); err == nil || !strings.Contains(err.Error(), "animated") {
		t.Fatalf("animated PNG accepted: %v", err)
	}
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x04, 0x00, 0x00, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x01, 0x2C, 0x01, 0x90, 0x03}
	decoded, err := DecodeImage(jpeg)
	if err != nil || decoded.Format != "jpeg" || decoded.Width != 400 || decoded.Height != 300 {
		t.Fatalf("JPEG header not read: %+v %v", decoded, err)
	}
}

func TestFitScaleStaysInsideLimits(t *testing.T) {
	cases := [][2]float64{{1503, 891}, {3006, 1782}, {5000, 400}, {400, 5000}, {2560, 1440}}
	for _, size := range cases {
		scale := FitScale(size[0], size[1])
		width, height := size[0]*scale, size[1]*scale
		if scale > 1 || width > MaxImageSide+0.5 || height > MaxImageSide+0.5 || width*height > MaxImagePixels+1 {
			t.Fatalf("%v scaled to %.3f gives %.0fx%.0f", size, scale, width, height)
		}
	}
	if FitScale(1000, 800) != 1 {
		t.Fatal("small images must not be scaled")
	}
}

func TestAskSendsDocumentedImageContract(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key", Model: "jev-1.14.0"})
	picture := testPNG(t, 400, 300)
	var seenHeader string
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		seenHeader = r.Header.Get(ImagesHeader)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		var assets map[string]map[string]string
		if err := json.Unmarshal(body["assets"], &assets); err != nil || assets["shot-1"]["type"] != "image" || assets["shot-1"]["format"] != "png" || assets["shot-1"]["data_base64"] == "" {
			t.Fatalf("assets not encoded as documented: %s", body["assets"])
		}
		var questions map[string]map[string]any
		if err := json.Unmarshal(body["questions"], &questions); err != nil {
			t.Fatal(err)
		}
		if _, hasCriteria := questions["where"]["criteria"]; hasCriteria || questions["where"]["type"] != "box" {
			t.Fatalf("box question must omit criteria: %v", questions["where"])
		}
		if _, hasCriteria := questions["present"]["criteria"]; hasCriteria || questions["present"]["type"] != "noul" {
			t.Fatalf("plain noul question must omit criteria: %v", questions["present"])
		}
		return httpResponse(r, 200, `{"model":"jev-1.14.0","answers":{"where":{"type":"box","box":{"top":0.1,"bottom":0.3,"left":0.2,"right":0.5},"confidence":0.77},"present":{"type":"noul","noul":0.93}},"usage":{"input_tokens":1200,"output_tokens":0}}`), nil
	})
	response, err := client.Ask(context.Background(), AskRequest{
		State: map[string]any{"screenshot": "shot-1"},
		Questions: map[string]TypedQuestion{
			"where":   {Type: "box", Instructions: "the Save button in `screenshot`"},
			"present": {Type: "noul", Instructions: "Is a Save button visible in `screenshot`?"},
		},
		Assets: map[string]Image{"shot-1": {Data: picture}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenHeader != "true" {
		t.Fatal("danger-zone images header missing")
	}
	where := response.Answers["where"]
	if where.Box == nil || where.Box.Left != 0.2 || where.Box.Right != 0.5 || where.Confidence == nil || *where.Confidence != 0.77 {
		t.Fatalf("box answer lost: %+v", where)
	}
	if response.Answers["present"].Noul == nil || *response.Answers["present"].Noul != 0.93 || response.Usage.InputTokens != 1200 {
		t.Fatalf("noul answer lost: %+v", response)
	}
}

func TestAskRejectsUnreferencedAssetsAndBadAnswers(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key"})
	picture := testPNG(t, 400, 300)
	_, err := client.Ask(context.Background(), AskRequest{State: "no reference here", Questions: map[string]TypedQuestion{"where": {Type: "box", Instructions: "the button"}}, Assets: map[string]Image{"shot": {Data: picture}}})
	if err == nil || CodeOf(err) != CodeInvalidRequest {
		t.Fatalf("unreferenced asset accepted: %v", err)
	}
	questions := map[string]TypedQuestion{"where": {Type: "box", Instructions: "the button in `shot`"}, "yes": {Type: "noul", Instructions: "Visible in `shot`?"}}
	cases := map[string]string{
		"inverted box":    `{"model":"jev-1.14.0","answers":{"where":{"type":"box","box":{"top":0.5,"bottom":0.2,"left":0.2,"right":0.5}},"yes":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`,
		"edge over one":   `{"model":"jev-1.14.0","answers":{"where":{"type":"box","box":{"top":0.1,"bottom":1.2,"left":0.2,"right":0.5}},"yes":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`,
		"type mismatch":   `{"model":"jev-1.14.0","answers":{"where":{"type":"noul","noul":0.4},"yes":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`,
		"missing answer":  `{"model":"jev-1.14.0","answers":{"where":{"type":"box","box":{"top":0.1,"bottom":0.3,"left":0.2,"right":0.5}}},"usage":{"input_tokens":1,"output_tokens":0}}`,
		"noul over one":   `{"model":"jev-1.14.0","answers":{"where":{"type":"box","box":{"top":0.1,"bottom":0.3,"left":0.2,"right":0.5}},"yes":{"type":"noul","noul":1.5}},"usage":{"input_tokens":1,"output_tokens":0}}`,
		"bad confidence":  `{"model":"jev-1.14.0","answers":{"where":{"type":"box","box":{"top":0.1,"bottom":0.3,"left":0.2,"right":0.5},"confidence":2},"yes":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`,
		"untrusted model": `{"model":"free text","answers":{},"usage":{"input_tokens":1,"output_tokens":0}}`,
	}
	for name, body := range cases {
		if _, err := decodeResponse([]byte(body), questions); err == nil || CodeOf(err) != CodeInvalidResponse {
			t.Fatalf("%s: accepted (%v)", name, err)
		}
	}
	good := `{"model":"jev-1.14.0","answers":{"where":{"type":"box","box":{"top":0.1,"bottom":0.3,"left":0.2,"right":0.5},"confidence":null},"yes":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`
	response, err := decodeResponse([]byte(good), questions)
	if err != nil || response.Answers["where"].Confidence != nil {
		t.Fatalf("null box confidence must be accepted as absent: %v %+v", err, response.Answers["where"])
	}
}

func TestAskWithoutImagesOmitsDangerHeader(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key"})
	var header string
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		header = r.Header.Get(ImagesHeader)
		return httpResponse(r, 200, `{"model":"jev-1.14.0","answers":{"pick":{"type":"choice","choice":"yes","probabilities":{"yes":0.9,"no":0.1},"confidence":0.8}},"usage":{"input_tokens":20,"output_tokens":10}}`), nil
	})
	response, err := client.Ask(context.Background(), AskRequest{State: "example", Questions: map[string]TypedQuestion{"pick": {Type: "choice", Instructions: "Choose", Choices: map[string]string{"yes": "Matches", "no": "Does not"}}}})
	if err != nil || header != "" || response.Answers["pick"].Choice != "yes" {
		t.Fatalf("plain choice request changed: %v header=%q %+v", err, header, response.Answers["pick"])
	}
}
