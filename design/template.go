// SPDX-License-Identifier: Apache-2.0

// Package generatedui supplies the fixed local template shared by drawn mocks
// and mini apps. Generated files cannot replace template assets.
package generatedui

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"html"
)

//go:embed tokens/tokens.css template/base.css template/mock.js fonts/PretendardVariable.woff2 fonts/OFL.txt
var files embed.FS

func mustRead(name string) []byte {
	b, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}
func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(h[:]) + "'"
}
func CSS() string {
	return string(mustRead("tokens/tokens.css")) + "\n" + string(mustRead("template/base.css"))
}
func FontLicense() []byte { return mustRead("fonts/OFL.txt") }
func Script() string      { return string(mustRead("template/mock.js")) }
func fontCSS(src string) string {
	return "@font-face { font-family: Pretendard; font-style: normal; font-weight: 100 900; font-display: block; src: url('" + src + "') format('woff2'); }\n"
}

// MockPage accepts only the validated fragment from the mock checker. All active
// code and style comes from this fixed template, never from the generated page.
func MockPage(title, fragment string) string {
	css := fontCSS("data:font/woff2;base64,"+base64.StdEncoding.EncodeToString(mustRead("fonts/PretendardVariable.woff2"))) + CSS()
	script := Script()
	policy := "default-src 'none'; base-uri 'none'; form-action 'none'; connect-src 'none'; object-src 'none'; frame-src 'none'; style-src " + hash(css) + "; script-src " + hash(script) + "; font-src data:; img-src 'none'"
	return "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta http-equiv=\"Content-Security-Policy\" content=\"" + html.EscapeString(policy) + "\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>" + html.EscapeString(title) + "</title><style>" + css + "</style></head><body>" + fragment + "<script>" + script + "</script></body></html>"
}

// MockFrame grants only scripts, with an opaque origin and no navigation,
// popups, downloads, forms or bridge. Clients must retain this sandbox attribute.
func MockFrame(page string, width int) string {
	return mockFrame(`srcdoc="`+html.EscapeString(page)+`"`, width)
}

// MockBundleFrame opens only the adjacent checked page within the exported bundle.
func MockBundleFrame(width int) string { return mockFrame(`src="index.html"`, width) }
func mockFrame(source string, width int) string {
	if width != 390 && width != 1280 {
		width = 390
	}
	size := "390"
	if width == 1280 {
		size = "1280"
	}
	return "<!doctype html><html><head><meta charset=\"utf-8\"></head><body style=\"margin:0\"><iframe title=\"Mock preview\" sandbox=\"allow-scripts\" referrerpolicy=\"no-referrer\" width=\"" + size + "\" height=\"900\" style=\"border:0;display:block\" " + source + "></iframe></body></html>"
}

// MiniAppFiles is the immutable template part of a mini-app bundle. A builder
// adds app.html/app.js, keeping these bytes unchanged. Gateway code belongs to
// the mini-app host, not to mocks.
func MiniAppFiles() map[string][]byte {
	return map[string][]byte{
		"OFL.txt":                  FontLicense(),
		"tokens.css":               mustRead("tokens/tokens.css"),
		"base.css":                 []byte(fontCSS("./PretendardVariable.woff2") + string(mustRead("template/base.css"))),
		"PretendardVariable.woff2": mustRead("fonts/PretendardVariable.woff2"),
		"index.html":               []byte("<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><link rel=\"stylesheet\" href=\"tokens.css\"><link rel=\"stylesheet\" href=\"base.css\"></head><body><main id=\"app\"></main><script src=\"app.js\"></script></body></html>"),
	}
}
