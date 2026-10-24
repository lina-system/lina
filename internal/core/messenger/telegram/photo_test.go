// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

// A photo whose largest representation is progressive downloads that one, not
// the small thumbnail beside it.
func TestProgressivePhotoSelectsTheFullImage(t *testing.T) {
	a := &Adapter{attachments: map[string]tg.InputFileLocationClass{}}
	photo := &tg.Photo{ID: 7, AccessHash: 8, FileReference: []byte("ref"), DCID: 2, Sizes: []tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1}},
		&tg.PhotoSize{Type: "m", W: 320, H: 240, Size: 9000},
		&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 960, Sizes: []int{4000, 20000, 61000}},
	}}
	out := a.attachmentsLocked(&tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 2}, Media: &tg.MessageMediaPhoto{Photo: photo}})
	if len(out) != 1 || out[0].Size != 61000 {
		t.Fatalf("attachment = %+v", out)
	}
	location, ok := a.attachments["photo:7"].(*tg.InputPhotoFileLocation)
	if !ok || location.ThumbSize != "y" {
		t.Fatalf("download location = %#v", a.attachments["photo:7"])
	}
}
