package engine

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ---- test kutu kurucuları ----

func box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, typ...), body...)
}

func u32(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.BigEndian.AppendUint32(b, x)
	}
	return b
}

func fullbox(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	return box(typ, append([]byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}, bytes.Join(parts, nil)...))
}

// testInit: tek izli init segmenti (ftyp+moov). entry: stsd örnek girdisi türü ("avc1", "encv", "mp4a").
func testInit(trackID, timescale uint32, entry string, extra ...[]byte) []byte {
	mvhd := fullbox("mvhd", 0, 0, u32(0, 0, 1000, 0), make([]byte, 76), u32(trackID+1)) // son 4 bayt next_track_ID
	tkhd := fullbox("tkhd", 0, 3, u32(0, 0, trackID, 0, 0), make([]byte, 60))
	mdhd := fullbox("mdhd", 0, 0, u32(0, 0, timescale, 0), make([]byte, 4))
	stsd := fullbox("stsd", 0, 0, u32(1), box(entry, make([]byte, 78)))
	trak := box("trak", tkhd, box("edts", fullbox("elst", 0, 0, u32(0))), box("mdia", mdhd, box("minf", box("stbl", stsd))))
	mvex := box("mvex", fullbox("mehd", 0, 0, u32(5000)), fullbox("trex", 0, 0, u32(trackID, 1, 0, 0, 0)))
	moov := box("moov", append([][]byte{mvhd, trak, mvex, box("udta", []byte("x"))}, extra...)...)
	return append(box("ftyp", []byte("iso6"), u32(0)), moov...)
}

// testFrag: moof (mfhd+traf(tfhd+tfdt+trun)) + mdat.
func testFrag(seq, trackID uint32, tfhdFlags uint32, t uint64, data string) []byte {
	tfdt := fullbox("tfdt", 1, 0, binary.BigEndian.AppendUint64(nil, t))
	trun := fullbox("trun", 0, 1, u32(1, 0)) // data_offset sonra yazılır
	traf := box("traf", fullbox("tfhd", 0, tfhdFlags, u32(trackID)), tfdt, trun)
	moof := box("moof", fullbox("mfhd", 0, 0, u32(seq)), traf)
	// data_offset = moof boyu + mdat başlığı (default-base-is-moof)
	binary.BigEndian.PutUint32(moof[len(moof)-4:], uint32(len(moof)+8))
	return append(moof, box("mdat", []byte(data))...)
}

type tbox struct {
	typ  string
	body []byte
}

func topBoxes(t *testing.T, b []byte) []tbox {
	t.Helper()
	var out []tbox
	for len(b) > 0 {
		n := binary.BigEndian.Uint32(b)
		if n < 8 || int(n) > len(b) {
			t.Fatalf("bozuk kutu: %d/%d", n, len(b))
		}
		out = append(out, tbox{string(b[4:8]), b[8:n]})
		b = b[n:]
	}
	return out
}

func findBox(t *testing.T, b []byte, path ...string) []byte {
	t.Helper()
	for _, p := range path {
		var next []byte
		for _, c := range topBoxes(t, b) {
			if c.typ == p {
				next = c.body
				break
			}
		}
		if next == nil {
			t.Fatalf("%s yok", p)
		}
		b = next
	}
	return b
}

func types(bs []tbox) []string {
	var s []string
	for _, b := range bs {
		s = append(s, b.typ)
	}
	return s
}

func TestWriteFMP4MergesTwoTracks(t *testing.T) {
	video := append(testInit(1, 1000, "avc1"), testFrag(1, 1, 0x020000, 0, "V0")...)
	video = append(video, box("styp", []byte("msdh"))...)
	video = append(video, testFrag(2, 1, 0x020000, 1000, "V1")...)
	audio := append(testInit(1, 48000, "mp4a"), box("sidx", make([]byte, 24))...)
	audio = append(audio, testFrag(7, 1, 0x020000, 0, "A0")...)
	audio = append(audio, testFrag(8, 1, 0x020000, 48000, "A1")...)
	var out bytes.Buffer
	if err := WriteFMP4(&out, bytes.NewReader(video), bytes.NewReader(audio)); err != nil {
		t.Fatal(err)
	}
	top := topBoxes(t, out.Bytes())
	want := []string{"ftyp", "moov", "moof", "mdat", "moof", "mdat", "moof", "mdat", "moof", "mdat"}
	if got := types(top); len(got) != len(want) || got[2] != "moof" || got[9] != "mdat" {
		t.Fatalf("kutular: %v", got)
	}
	var mdats string
	for i, b := range top {
		if b.typ == "mdat" {
			mdats += string(b.body)
		}
		if b.typ == "moof" {
			seq := binary.BigEndian.Uint32(findBox(t, b.body, "mfhd")[4:])
			if int(seq) != i/2 {
				t.Errorf("mfhd sıra %d, %d bekleniyordu", seq, i/2)
			}
			id := binary.BigEndian.Uint32(findBox(t, b.body, "traf", "tfhd")[4:])
			if wantID := map[byte]uint32{'V': 1, 'A': 2}[top[i+1].body[0]]; id != wantID {
				t.Errorf("tfhd track_ID %d, %d bekleniyordu", id, wantID)
			}
		}
	}
	if mdats != "V0A0V1A1" {
		t.Fatalf("zaman sırası: %s", mdats)
	}
	moov := top[1].body
	kids := types(topBoxes(t, moov))
	if len(kids) != 4 || kids[0] != "mvhd" || kids[1] != "trak" || kids[2] != "trak" || kids[3] != "mvex" {
		t.Fatalf("moov: %v", kids)
	}
	mvhd := findBox(t, moov, "mvhd")
	if binary.BigEndian.Uint32(mvhd[len(mvhd)-4:]) != 3 {
		t.Fatal("next_track_ID 3 değil")
	}
	var ids []uint32
	for _, c := range topBoxes(t, moov) {
		if c.typ == "trak" {
			ids = append(ids, binary.BigEndian.Uint32(findBox(t, c.body, "tkhd")[12:]))
			findBox(t, c.body, "edts") // korunur
		}
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("tkhd kimlikleri: %v", ids)
	}
	mvex := topBoxes(t, findBox(t, moov, "mvex"))
	if len(mvex) != 2 || mvex[0].typ != "trex" || binary.BigEndian.Uint32(mvex[1].body[4:]) != 2 {
		t.Fatalf("mvex: %v", types(mvex))
	}
	// trun.data_offset dokunulmadı ve hâlâ kendi mdat'ının verisini gösteriyor.
	moof := top[2]
	off := binary.BigEndian.Uint32(moof.body[len(moof.body)-4:])
	if int(off) != len(moof.body)+8+8 {
		t.Fatalf("data_offset %d", off)
	}
}

func TestWriteFMP4SingleTrackCleans(t *testing.T) {
	in := append(testInit(1, 1000, "avc1"), box("sidx", make([]byte, 24))...)
	in = append(in, testFrag(1, 1, 0x020000, 0, "V0")...)
	in = append(in, box("styp", []byte("msdh"))...)
	in = append(in, box("emsg", []byte("x"))...)
	in = append(in, testFrag(2, 1, 0x020000, 1000, "V1")...)
	in = append(in, box("mfra", []byte("x"))...)
	var out bytes.Buffer
	if err := WriteFMP4(&out, bytes.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if got := types(topBoxes(t, out.Bytes())); len(got) != 6 || got[0] != "ftyp" || got[1] != "moov" || got[5] != "mdat" {
		t.Fatalf("kutular: %v", got)
	}
}

func TestWriteFMP4Rejects(t *testing.T) {
	video := append(testInit(1, 1000, "avc1"), testFrag(1, 1, 0x1, 0, "V0")...) // base-data-offset-present
	audio := append(testInit(1, 48000, "mp4a"), testFrag(1, 1, 0x020000, 0, "A0")...)
	if err := WriteFMP4(&bytes.Buffer{}, bytes.NewReader(video), bytes.NewReader(audio)); err == nil {
		t.Fatal("mutlak ofsetli parça birleştirildi")
	}
	plain := append(box("ftyp", []byte("isom"), u32(0)), box("moov", fullbox("mvhd", 0, 0, make([]byte, 96)))...)
	plain = append(plain, box("mdat", []byte("x"))...)
	if err := WriteFMP4(&bytes.Buffer{}, bytes.NewReader(plain)); err == nil {
		t.Fatal("parçasız MP4 kabul edildi")
	}
}

func TestEncryptedInit(t *testing.T) {
	if encryptedInit(testInit(1, 1000, "avc1")) {
		t.Fatal("şifresiz init korumalı sayıldı")
	}
	if !encryptedInit(testInit(1, 1000, "encv")) || !encryptedInit(testInit(1, 48000, "enca")) {
		t.Fatal("encv/enca görülmedi")
	}
	if !encryptedInit(testInit(1, 1000, "avc1", fullbox("pssh", 0, 0, make([]byte, 20)))) {
		t.Fatal("pssh görülmedi")
	}
}
