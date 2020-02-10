package mp4

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"time"
)

// decodeUint32 is a Uint32 wrapper.  The compiler is likely to inline.
func decodeUint32(data []byte) uint32 {
	return binary.BigEndian.Uint32(data)
}

// decodeUint64 is a Uint64 wrapper.
func decodeUint64(data []byte) uint64 {
	return binary.BigEndian.Uint64(data)
}

// Type is the 4-byte atom type code.
type Type []byte

func (t Type) String() string {
	return string(t[:4])
}

// isItunesMetaDataContainer returns true if t is a container with a
// 'data' atom inside.
func isItunesMetaDataContainer(t Type) bool {
	switch string(t) {
	case
		"\xa9nam", // ©nam
		"\xa9ART", // ©ART
		"aART",
		"\xa9alb", // ©alb
		"gnre",
		"\xa9gen", // ©gen
		"\xa9day", // ©day
		"trkn",
		"disk",
		"tvsh",
		"tven",
		"tvsn",
		"tves",
		"desc",
		"ldes",
		"sdes",
		"\xa9too", // ©too
		"stik",
		"hdvd",
		"sfID",
		"cnID",
		"atID",
		"plID",
		"covr":
		return true
	}
	return false
}

// isContainerType returns true if t is a well known container type.
func isContainer(t Type) bool {
	switch string(t) {
	case
		"moov",
		"trak",
		"udta",
		"meta",
		"ilst":
		return true
	}
	return isItunesMetaDataContainer(t)
}

// Atom is a node in the MP4 object hierarchy.
type Atom struct {
	Start     int64  // position in file
	Size      int64  // outer size of atom
	RawHeader []byte // preserved exactly for future writing out
	Type      Type
	Container bool // has atoms within
	Skip      bool // ignore rest of atom

	ancestors []*Atom
	rd        io.Reader
	consumed  int64 // amount of raw atom read, including header: rd.pos - Start
}

// voidReadSize is the disposable array size for simulating Seek via Read.
const voidReadSize = 1 << 16

// skip advances rd by count bytes.
func skip(rd io.Reader, count int64) error {
	if count <= 0 {
		return nil
	}

	if seeker, ok := rd.(io.Seeker); ok {
		_, err := seeker.Seek(count, io.SeekCurrent)
		return err
	}

	// fall back to reading disposable chunks
	buffer := make([]byte, voidReadSize)
	for count > 0 {
		if count < int64(len(buffer)) {
			// fewer remaining to read
			buffer = buffer[:count]
		}
		if n, err := rd.Read(buffer); err != nil {
			return err
		} else {
			count -= int64(n)
		}
	}
	return nil
}

// Read part of the atom, but no further.
func (a *Atom) Read(buffer []byte) (n int, err error) {
	remain := a.Size - a.consumed
	if remain <= 0 {
		return 0, io.EOF
	}
	// guard against overreading
	if remain < int64(len(buffer)) {
		buffer = buffer[:remain]
	}
	n, err = a.rd.Read(buffer)
	a.consumed += int64(n)
	return
}

// MustRead detects unexpected partial reads.
func (a *Atom) MustRead(buffer []byte) error {
	n, err := a.Read(buffer)
	if err != nil {
		return err
	}
	if n < len(buffer) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

const (
	minimalHeaderSize = 8
	extendedSizeSize  = 8
	metaSubheaderSize = 4
)

// readAtomHeader builds a new Atom from the next bytes of rd.
// Minimal interpretation is done to recognize well-known containers.
func readAtomHeader(rd io.Reader) (*Atom, error) {
	a := &Atom{
		Size:      minimalHeaderSize,
		RawHeader: make([]byte, minimalHeaderSize),
		rd:        rd,
	}
	if err := a.MustRead(a.RawHeader); err != nil {
		return nil, err
	}

	a.Type = Type(a.RawHeader[4:8])
	a.Container = isContainer(a.Type)
	sz := int64(decodeUint32(a.RawHeader[0:4]))
	if sz == 1 {
		offset := len(a.RawHeader)
		a.Size += extendedSizeSize
		a.RawHeader = append(a.RawHeader, make([]byte, extendedSizeSize)...)
		if err := a.MustRead(a.RawHeader[offset:]); err != nil {
			return nil, err
		}
		sz = int64(decodeUint64(a.RawHeader[offset:]))
	}
	a.Size = sz

	// containers with additional implicit header
	if string(a.Type) == "meta" {
		offset := len(a.RawHeader)
		a.Size += metaSubheaderSize
		a.RawHeader = append(a.RawHeader, make([]byte, metaSubheaderSize)...)
		if err := a.MustRead(a.RawHeader[offset:]); err != nil {
			return nil, err
		}
	}

	return a, nil
}

// WalkFunc is a callback function to apply to each node of the MP4
// object hierarchy.  It may:
//   1) return without error, signaling continued processing
//   2) return with an error, aborting further traversal
//   3) read the atom in-the-raw
//   4) mark the atom (or an ancestor) as skippable
//   5) start another walk, possibly with a different WalkFunc
type WalkFunc func(ancestors []*Atom, a *Atom) (err error)

// Walk recursively traverses the file.
func (a *Atom) Walk(walkFunc WalkFunc) error {
	if !a.Container {
		a.Skip = true
	}

	if a.Skip {
		return nil
	}

	ancestors := append(a.ancestors, a)

	for a.consumed < a.Size {
		child, err := readAtomHeader(a.rd)
		if err != nil {
			return err
		}

		child.Start = a.Start + a.consumed
		child.ancestors = ancestors

		a.consumed += child.Size

		// rd points to end of child's standard header.  The walkFunc
		// could call Read() or Walk() methods, or set Skip or
		// Container attributes.

		if err := walkFunc(ancestors, child); err != nil {
			return err
		}

		// rd points somewhere after child's standard header.  If
		// child is also a container, rd should not point somewhere
		// within a children, but either before or after (and perhaps
		// between).

		if err := child.Walk(walkFunc); err != nil {
			return err
		}

		if child.Skip {
			discard := child.Size - child.consumed
			if err := skip(child.rd, discard); err != nil {
				return err
			}
		}

		// rd points to start of next child.
	}

	return nil
}

// Root is a top-level rest-of-file pseudo-atom.
func Root(rd io.Reader) *Atom {
	size := int64(math.MaxInt64)

	// attempt to measure via seek hack
	if rd, ok := rd.(io.Seeker); ok {
		size, _ = rd.Seek(0, io.SeekEnd)
		rd.Seek(0, io.SeekStart)
	}

	return &Atom{
		Size:      size,
		Container: true,
		rd:        rd,
	}
}

// TolerateEOF returns all errors except EOF.
func TolerateEOF(err error) error {
	if err == io.EOF {
		return nil
	}
	return err
}

// Walk recursively applies a WalkFunc to atoms of the file rd.
func Walk(rd io.Reader, walkFunc WalkFunc) error {
	return TolerateEOF(Root(rd).Walk(walkFunc))
}

// ITunesMetadata is iTunes-introduced metadata.
type ITunesMetadata struct {
	Name              string `json:"name,omitempty",               ilst:©nam`
	Artist            string `json:"artist,omitempty",             ilst:©ART`
	AlbumArtist       string `json:"album_artist,omitempty",       ilst:aART`
	Album             string `json:"album,omitempty",              ilst:©alb`
	Genre             uint16 `json:"genre,omitempty",              ilst:gnre`
	CustomGenre       string `json:"genre,omitempty",              ilst:©gen`
	ReleaseDate       string `json:"release_date,omitempty",       ilst:©day`
	Track             uint16 `json:"track,omitempty",              ilst:trkn.0`
	Tracks            uint16 `json:"tracks,omitempty",             ilst:trkn.1`
	Disk              uint16 `json:"disk,omitempty",               ilst:disk.0`
	Disks             uint16 `json:"disks,omitempty",              ilst:disk.1`
	TVShow            string `json:"tv_show,omitempty",            ilst:tvsh`
	TVEpisodeId       string `json:"tv_episode_id,omitempty",      ilst:tven`
	TVSeason          uint32 `json:"tv_season,omitempty",          ilst:tvsn`
	TVEpisodeNumber   uint32 `json:"tv_episode,omitempty",         ilst:tves`
	Description       string `json:"description,omitempty",        ilst:desc`
	LongDescription   string `json:"long_description,omitempty",   ilst:ldes`
	SeriesDescription string `json:"series_description,omitempty", ilst:sdes`
	Encoder           string `json:"encoder,omitempty",            ilst:©too`
	MediaKind         byte   `json:"media_kind,omitempty",         ilst:stik`
	CountryId         uint32 `json:"country_id,omitempty",         ilst:sfID`
	ContentId         uint32 `json:"content_id,omitempty",         ilst:cnID`
	ArtistId          uint32 `json:"artist_id,omitempty",          ilst:atID`
	PlaylistId        uint64 `json:"playlist_id,omitempty",        ilst:plID`
	CoverArt          []byte `json:"cover_art,omitempty",          ilst:covr`
}

func (i *ITunesMetadata) Set(type_ Type, data []byte) error {
	expected := len(data)
	switch string(type_) {
	case "\xa9nam":
		i.Name = string(data)
	case "\xa9ART":
		i.Artist = string(data)
	case "aART":
		i.AlbumArtist = string(data)
	case "\xa9alb":
		i.Album = string(data)
	case "gnre":
		i.Genre = binary.BigEndian.Uint16(data)
		expected = 2
	case "\xa9gen":
		i.CustomGenre = string(data)
		// "Action & Adventure"
		// "Animation"
		// "Anime"
		// "Classics"
		// "Comedy"
		// "Concert Film"
		// "Documentary"
		// "Drama"
		// "Foreign"
		// "Horror"
		// "Independent"
		// "Kids & Family"
		// "Music Documentaries"
		// "Musicals"
		// "Nonfiction"
		// "Romance"
		// "Sci-Fi & Fantasy"
		// "Special Interest"
		// "Sports"
		// "Thriller"
		// "Western"
	case "\xa9day":
		i.ReleaseDate = string(data)
	case "trkn":
		// binary.BigEndianUint16(data[0:2])
		i.Track = binary.BigEndian.Uint16(data[2:4])
		i.Tracks = binary.BigEndian.Uint16(data[4:6])
		// binary.BigEndianUint16(data[6:8])
		expected = 8
	case "disk":
		// binary.BigEndianUint16(data[0:2])
		i.Disk = binary.BigEndian.Uint16(data[2:4])
		i.Disks = binary.BigEndian.Uint16(data[4:6])
		expected = 6
	case "tvsh":
		i.TVShow = string(data)
	case "tven":
		i.TVEpisodeId = string(data)
	case "tvsn":
		i.TVSeason = decodeUint32(data)
		expected = 4
	case "tves":
		i.TVEpisodeNumber = decodeUint32(data)
		expected = 4
	case "desc":
		i.Description = string(data)
	case "ldes":
		i.LongDescription = string(data)
	case "sdes":
		i.SeriesDescription = string(data)
	case "\xa9too":
		i.Encoder = string(data)
	case "stik":
		i.MediaKind = data[0]
		// 9 Movie
		// 10 TV Show
		expected = 1
	case "sfID":
		i.CountryId = decodeUint32(data)
		expected = 4
	case "cnID":
		i.ContentId = decodeUint32(data)
		expected = 4
	case "atID":
		i.ArtistId = decodeUint32(data)
		expected = 4
	case "plID":
		i.PlaylistId = decodeUint64(data)
		expected = 8
	case "covr":
		//i.CoverArt = data
	case "hdvd":
		//i.Definition = ...
	}
	if expected != len(data) {
		return fmt.Errorf("metadata %s wrong size", type_)
	}
	return nil
}

func (i *ITunesMetadata) UplookingRead(rd io.Reader) error {
	return Walk(rd, func(ancestors []*Atom, a *Atom) error {
		if string(a.Type) == "data" && len(ancestors) > 0 {
			data, err := ioutil.ReadAll(a)
			if err != nil {
				return err
			}
			parent := ancestors[len(ancestors)-1]
			return i.Set(parent.Type, data[8:])
		}
		return nil
	})
}

func (i *ITunesMetadata) Read(rd io.Reader) error {
	return Walk(rd, func(_ []*Atom, parent *Atom) error {
		if isItunesMetaDataContainer(parent.Type) {
			return parent.Walk(func(_ []*Atom, a *Atom) error {
				if string(a.Type) == "data" {
					data, err := ioutil.ReadAll(a)
					if err != nil {
						return err
					}
					return i.Set(parent.Type, data[8:])
				}
				return nil
			})
		}
		return nil
	})
}

// MVHD is movie header metadata.
type MVHD struct {
	Version   byte
	TimeScale uint32
	Duration  uint64
}

func (mvhd *MVHD) Read(rd io.Reader) error {
	return Walk(rd, func(_ []*Atom, a *Atom) error {
		if string(a.Type) == "mvhd" {
			data, err := ioutil.ReadAll(a)
			if err != nil {
				return err
			}
			mvhd.Version = data[0]
			mvhd.TimeScale = decodeUint32(data[12:16])
			mvhd.Duration = uint64(decodeUint32(data[16:20]))
			if mvhd.Version == 1 {
				mvhd.TimeScale = decodeUint32(data[20:24])
				mvhd.Duration = decodeUint64(data[24:32])
			}
			duration := time.Duration(float32(mvhd.Duration)/float32(mvhd.TimeScale)) * time.Second
			fmt.Printf("mvhd %s\n", duration)
		}
		return nil
	})
}

// TKHD is track header metadata.
type TKHD struct {
	Version          byte `json:"version,omitempty"`
	Flags            uint32
	CreationTime     uint32 // seconds since 1904
	ModificationTime uint32
	TrackID          uint32
	reserved         [4]byte
	Duration         uint32
	reserved2        [8]byte
	Layer            uint16
	AlternateGroup   uint16
	Volume           uint16
	reserved3        [2]byte
	MatrixStructure  [36]byte `json:"matrix_structure,omitempty"`
	TrackWidth       FixedFloat32
	TrackHeight      FixedFloat32
}

type FixedFloat32 struct {
	Integer  uint16
	Fraction uint16
}

func (ff FixedFloat32) Float32() float32 {
	return float32(ff.Integer) + float32(ff.Fraction)/math.MaxUint16
}

func NewFixedFloat32(data []byte) FixedFloat32 {
	return FixedFloat32{
		binary.BigEndian.Uint16(data[0:2]),
		binary.BigEndian.Uint16(data[2:4]),
	}
}

func (tkhd *TKHD) Read(rd io.Reader) error {
	return Walk(rd, func(_ []*Atom, a *Atom) error {
		// XXX how to skip subsequent tkhds?
		if string(a.Type) == "tkhd" {
			data, err := ioutil.ReadAll(a)
			if err != nil {
				return err
			}
			tkhd.Version = data[0]
			tkhd.TrackWidth = NewFixedFloat32(data[76:80])
			tkhd.TrackHeight = NewFixedFloat32(data[80:84])
		}
		return nil
	})
}

// TypePath maps a slice of atoms to their types.
func TypePath(ancestors []*Atom) []Type {
	var path []Type
	for _, ancestor := range ancestors {
		if ancestor.Type != nil {
			path = append(path, ancestor.Type)
		}
	}
	return path
}
