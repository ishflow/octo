package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

// Parçalı MP4 (ISO/IEC 14496-12) kutu düzeyinde temizleme ve ses+görüntü birleştirme. Örnek düzeyine
// inilmez: moof+mdat çiftleri olduğu gibi kopyalanır, yerinde yalnız mfhd.sequence_number ve tfhd.track_ID
// yazılır (boyut değişmez → trun.data_offset geçerli kalır). Bellekte yalnız moov ve tek moof tutulur.

var errNotFragmented = errors.New("Video parçalı MP4 değil; birleştirilemedi")

// fileBox girdideki üst düzey kutudur.
type fileBox struct {
	typ       string
	off, size int64 // başlık dahil
}

// rawBox bellekteki bir kutudur; body başlıktan sonrası.
type rawBox struct {
	typ  string
	full []byte // başlık dahil
	body []byte
}

func scanBoxes(r io.ReadSeeker) ([]fileBox, error) {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	var out []fileBox
	var hdr [16]byte
	for off := int64(0); off < end; {
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, hdr[:8]); err != nil {
			return nil, errNotFragmented
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		switch size {
		case 0:
			size = end - off
		case 1:
			if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
				return nil, errNotFragmented
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
		}
		if size < 8 || off+size > end {
			return nil, fmt.Errorf("bozuk MP4 kutusu (%q)", hdr[4:8])
		}
		out = append(out, fileBox{string(hdr[4:8]), off, size})
		off += size
	}
	return out, nil
}

// children bellekteki kutunun çocuklarını verir (32 ve 64 bit boy).
func children(b []byte) ([]rawBox, error) {
	var out []rawBox
	for len(b) > 0 {
		if len(b) < 8 {
			return nil, errNotFragmented
		}
		n, hdr := uint64(binary.BigEndian.Uint32(b)), uint64(8)
		switch n {
		case 0:
			n = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return nil, errNotFragmented
			}
			n, hdr = binary.BigEndian.Uint64(b[8:]), 16
		}
		if n < hdr || n > uint64(len(b)) {
			return nil, fmt.Errorf("bozuk MP4 kutusu (%q)", b[4:8])
		}
		out = append(out, rawBox{typ: string(b[4:8]), full: b[:n], body: b[hdr:n]})
		b = b[n:]
	}
	return out, nil
}

func child(b []byte, path ...string) []byte {
	for _, p := range path {
		kids, err := children(b)
		if err != nil {
			return nil
		}
		i := slices.IndexFunc(kids, func(k rawBox) bool { return k.typ == p })
		if i < 0 {
			return nil
		}
		b = kids[i].body
	}
	return b
}

func readBox(r io.ReadSeeker, b fileBox) ([]byte, error) {
	if b.size > maxManifest { // moov/moof bu kadar büyük olmaz; bellek sınırı
		return nil, fmt.Errorf("MP4 kutusu çok büyük (%s)", b.typ)
	}
	buf := make([]byte, b.size)
	if _, err := r.Seek(b.off, io.SeekStart); err != nil {
		return nil, err
	}
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func mkBox(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	b := binary.BigEndian.AppendUint32(make([]byte, 0, n), uint32(n))
	b = append(b, typ...)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// encryptedInit: init segmentinde şifreli örnek girdisi (encv/enca, sinf) ya da pssh var mı (K31).
// Ayrıştırılamayan init korumalı sayılır (fail-closed, VGR M2).
func encryptedInit(init []byte) bool {
	var walk func(b []byte) bool
	walk = func(b []byte) bool {
		kids, err := children(b)
		if err != nil {
			return true
		}
		for _, k := range kids {
			switch k.typ {
			case "pssh", "encv", "enca", "sinf":
				return true
			case "moov", "trak", "mdia", "minf", "stbl":
				if walk(k.body) {
					return true
				}
			case "stsd": // tam kutu + girdi sayısı, sonra örnek girdileri
				if len(k.body) > 8 && walk(k.body[8:]) {
					return true
				}
			}
		}
		return false
	}
	return walk(init)
}

type fmp4Input struct {
	r     io.ReadSeeker
	ftyp  []byte
	moov  []byte
	frags []fileBox // moof, hemen ardından mdat (çift olarak: frags[2i], frags[2i+1])
}

func readInput(r io.ReadSeeker) (*fmp4Input, error) {
	bs, err := scanBoxes(r)
	if err != nil {
		return nil, err
	}
	in := &fmp4Input{r: r}
	for i := 0; i < len(bs); i++ {
		switch b := bs[i]; b.typ {
		case "ftyp":
			if in.ftyp == nil {
				if in.ftyp, err = readBox(r, b); err != nil {
					return nil, err
				}
			}
		case "moov":
			if in.moov, err = readBox(r, b); err != nil {
				return nil, err
			}
		case "moof":
			if i+1 >= len(bs) || bs[i+1].typ != "mdat" {
				return nil, errors.New("MP4 parçasında veri (mdat) yok")
			}
			in.frags = append(in.frags, b, bs[i+1])
			i++
		} // styp, sidx, emsg, prft, free, skip, mfra, parçasız mdat atılır
	}
	if in.moov == nil || len(in.frags) == 0 {
		return nil, errNotFragmented
	}
	if encryptedInit(in.moov) {
		return nil, errors.New(errProtected)
	}
	return in, nil
}

// WriteFMP4 tek izi temizler (ftyp+moov+moof/mdat), iki izi (görüntü, ses) tek dosyada birleştirir.
func WriteFMP4(out io.Writer, tracks ...io.ReadSeeker) error {
	if len(tracks) == 0 || len(tracks) > 2 {
		return errors.New("WriteFMP4: 1 ya da 2 iz")
	}
	var ins []*fmp4Input
	for _, r := range tracks {
		in, err := readInput(r)
		if err != nil {
			return err
		}
		ins = append(ins, in)
	}
	v := ins[0]
	if len(ins) == 1 {
		ftyp := v.ftyp
		if _, err := out.Write(append(slices.Clip(ftyp), v.moov...)); err != nil {
			return err
		}
		for i := 0; i < len(v.frags); i += 2 {
			if err := copyFrag(out, v, i, 0, 0); err != nil {
				return err
			}
		}
		return nil
	}
	moov, scales, err := mergeMoov(ins)
	if err != nil {
		return err
	}
	if _, err := out.Write(append(slices.Clip(v.ftyp), moov...)); err != nil {
		return err
	}
	// İki izi tfdt zamanına göre iç içe sırala (eşitlikte önce görüntü); tfdt yoksa önce görüntü, sonra ses.
	type ref struct {
		in, i int
		t     float64
	}
	var all []ref
	timed := true
	for n, in := range ins {
		for i := 0; i < len(in.frags); i += 2 {
			moof, err := readBox(in.r, in.frags[i])
			if err != nil {
				return err
			}
			if err := checkMergeable(moof); err != nil {
				return err
			}
			t, ok := fragTime(moof)
			timed = timed && ok
			all = append(all, ref{n, i, float64(t) / float64(scales[n])})
		}
	}
	if timed {
		slices.SortStableFunc(all, func(a, b ref) int {
			if a.t != b.t {
				if a.t < b.t {
					return -1
				}
				return 1
			}
			return a.in - b.in
		})
	}
	for seq, f := range all {
		if err := copyFrag(out, ins[f.in], f.i, uint32(seq+1), uint32(f.in+1)); err != nil {
			return err
		}
	}
	return nil
}

// mergeMoov: mvhd videodan (next_track_ID=3), iki trak (tkhd.track_ID 1/2), mvex (iki trex, mehd yok).
func mergeMoov(ins []*fmp4Input) ([]byte, []uint32, error) {
	var traks, trexs [][]byte
	var scales []uint32
	var mvhd []byte
	for n, in := range ins {
		id := uint32(n + 1)
		kids, err := children(in.moov[8:])
		if err != nil {
			return nil, nil, err
		}
		var trak, trex []byte
		for _, k := range kids {
			switch k.typ {
			case "mvhd":
				if n == 0 {
					mvhd = slices.Clone(k.full)
				}
			case "trak":
				if trak != nil {
					return nil, nil, errors.New("Birden çok izli parça birleştirilemez")
				}
				trak = slices.Clone(k.full)
			case "mvex":
				if trex = child(k.body, "trex"); trex != nil {
					trex = mkBox("trex", trex)
				}
			}
		}
		if trak == nil || trex == nil || len(trex) < 16 {
			return nil, nil, errNotFragmented
		}
		tkhd := child(trak[8:], "tkhd")
		mdhd := child(trak[8:], "mdia", "mdhd")
		if len(tkhd) < 28 || len(mdhd) < 24 {
			return nil, nil, errNotFragmented
		}
		idOff := 12 // tkhd v0: sürüm/bayrak, oluşturma, değiştirme (4'er bayt)
		tsOff := 12
		if tkhd[0] == 1 {
			idOff = 20
		}
		if mdhd[0] == 1 {
			tsOff = 20
		}
		binary.BigEndian.PutUint32(tkhd[idOff:], id) // tkhd trak'ın içinde: yerinde yazılır
		binary.BigEndian.PutUint32(trex[12:], id)    // başlık 8 + sürüm/bayrak 4
		scale := binary.BigEndian.Uint32(mdhd[tsOff:])
		if scale == 0 {
			return nil, nil, errNotFragmented
		}
		traks, trexs, scales = append(traks, trak), append(trexs, trex), append(scales, scale)
	}
	if len(mvhd) < 12 {
		return nil, nil, errNotFragmented
	}
	binary.BigEndian.PutUint32(mvhd[len(mvhd)-4:], uint32(len(ins)+1)) // next_track_ID: son alan
	return mkBox("moov", append(append([][]byte{mvhd}, traks...), mkBox("mvex", trexs...))...), scales, nil
}

// checkMergeable: tek traf, mutlak ofset (base-data-offset-present) yok.
func checkMergeable(moof []byte) error {
	kids, err := children(moof[8:])
	if err != nil {
		return err
	}
	trafs := 0
	for _, k := range kids {
		if k.typ != "traf" {
			continue
		}
		trafs++
		tfhd := child(k.body, "tfhd")
		if len(tfhd) < 8 || tfhd[3]&0x1 != 0 {
			return errors.New("Bu akışın parçaları birleştirilemiyor (mutlak ofset)")
		}
	}
	if trafs != 1 {
		return errors.New("Birden çok izli parça birleştirilemez")
	}
	return nil
}

func fragTime(moof []byte) (uint64, bool) {
	tfdt := child(moof[8:], "traf", "tfdt")
	switch {
	case len(tfdt) >= 12 && tfdt[0] == 1:
		return binary.BigEndian.Uint64(tfdt[4:]), true
	case len(tfdt) >= 8:
		return uint64(binary.BigEndian.Uint32(tfdt[4:])), true
	}
	return 0, false
}

// copyFrag moof+mdat çiftini yazar; seq/id 0 değilse mfhd.sequence_number ve tfhd.track_ID yerinde yazılır.
func copyFrag(out io.Writer, in *fmp4Input, i int, seq, id uint32) error {
	moof, err := readBox(in.r, in.frags[i])
	if err != nil {
		return err
	}
	if seq != 0 {
		if mfhd := child(moof[8:], "mfhd"); len(mfhd) >= 8 {
			binary.BigEndian.PutUint32(mfhd[4:], seq)
		}
		if tfhd := child(moof[8:], "traf", "tfhd"); len(tfhd) >= 8 {
			binary.BigEndian.PutUint32(tfhd[4:], id)
		}
	}
	if _, err := out.Write(moof); err != nil {
		return err
	}
	mdat := in.frags[i+1]
	if _, err := in.r.Seek(mdat.off, io.SeekStart); err != nil {
		return err
	}
	_, err = io.CopyN(out, in.r, mdat.size)
	return err
}
