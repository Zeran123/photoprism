package entity

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/photoprism/photoprism/pkg/clusters"

	"github.com/photoprism/photoprism/internal/face"
	"github.com/photoprism/photoprism/internal/form"
	"github.com/photoprism/photoprism/pkg/txt"
)

const (
	MarkerUnknown = ""
	MarkerFace    = "face"
	MarkerLabel   = "label"
)

// Marker represents an image marker point.
type Marker struct {
	ID             uint            `gorm:"primary_key" json:"ID" yaml:"-"`
	FileID         uint            `gorm:"index;" json:"-" yaml:"-"`
	MarkerType     string          `gorm:"type:VARBINARY(8);default:'';" json:"Type" yaml:"Type"`
	MarkerSrc      string          `gorm:"type:VARBINARY(8);default:'';" json:"Src" yaml:"Src,omitempty"`
	MarkerName     string          `gorm:"type:VARCHAR(255);" json:"Name" yaml:"Name,omitempty"`
	SubjectUID     string          `gorm:"type:VARBINARY(42);index;" json:"SubjectUID" yaml:"SubjectUID,omitempty"`
	SubjectSrc     string          `gorm:"type:VARBINARY(8);default:'';" json:"SubjectSrc" yaml:"SubjectSrc,omitempty"`
	Subject        *Subject        `gorm:"foreignkey:SubjectUID;association_foreignkey:SubjectUID;association_autoupdate:false;association_autocreate:false;association_save_reference:false" json:"Subject,omitempty" yaml:"-"`
	FaceID         string          `gorm:"type:VARBINARY(42);index;" json:"FaceID" yaml:"FaceID,omitempty"`
	FaceDist       float64         `gorm:"default:-1" json:"FaceDist" yaml:"FaceDist,omitempty"`
	Face           *Face           `gorm:"foreignkey:FaceID;association_foreignkey:ID;association_autoupdate:false;association_autocreate:false;association_save_reference:false" json:"-" yaml:"-"`
	EmbeddingsJSON json.RawMessage `gorm:"type:MEDIUMBLOB;" json:"-" yaml:"EmbeddingsJSON,omitempty"`
	embeddings     Embeddings      `gorm:"-"`
	LandmarksJSON  json.RawMessage `gorm:"type:MEDIUMBLOB;" json:"-" yaml:"LandmarksJSON,omitempty"`
	X              float32         `gorm:"type:FLOAT;" json:"X" yaml:"X,omitempty"`
	Y              float32         `gorm:"type:FLOAT;" json:"Y" yaml:"Y,omitempty"`
	W              float32         `gorm:"type:FLOAT;" json:"W" yaml:"W,omitempty"`
	H              float32         `gorm:"type:FLOAT;" json:"H" yaml:"H,omitempty"`
	Size           int             `gorm:"default:-1" json:"Size" yaml:"Size,omitempty"`
	Score          int             `gorm:"type:SMALLINT" json:"Score" yaml:"Score,omitempty"`
	MarkerInvalid  bool            `json:"Invalid" yaml:"Invalid,omitempty"`
	MatchedAt      *time.Time      `sql:"index" json:"MatchedAt" yaml:"MatchedAt,omitempty"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// UnknownMarker can be used as a default for unknown markers.
var UnknownMarker = NewMarker(0, "", SrcDefault, MarkerUnknown, 0, 0, 0, 0)

// TableName returns the entity database table name.
func (Marker) TableName() string {
	return "markers_dev5"
}

// NewMarker creates a new entity.
func NewMarker(fileID uint, subjectUID, markerSrc, markerType string, x, y, w, h float32) *Marker {
	m := &Marker{
		FileID:     fileID,
		SubjectUID: subjectUID,
		MarkerSrc:  markerSrc,
		MarkerType: markerType,
		X:          x,
		Y:          y,
		W:          w,
		H:          h,
	}

	return m
}

// NewFaceMarker creates a new entity.
func NewFaceMarker(f face.Face, fileID uint, refUID string) *Marker {
	pos := f.Marker()

	m := NewMarker(fileID, refUID, SrcImage, MarkerFace, pos.X, pos.Y, pos.W, pos.H)

	m.MatchedAt = nil
	m.FaceDist = -1
	m.EmbeddingsJSON = f.EmbeddingsJSON()
	m.LandmarksJSON = f.RelativeLandmarksJSON()
	m.Size = f.Size()
	m.Score = f.Score

	return m
}

// Updates multiple columns in the database.
func (m *Marker) Updates(values interface{}) error {
	return UnscopedDb().Model(m).Updates(values).Error
}

// Update updates a column in the database.
func (m *Marker) Update(attr string, value interface{}) error {
	return UnscopedDb().Model(m).Update(attr, value).Error
}

// SaveForm updates the entity using form data and stores it in the database.
func (m *Marker) SaveForm(f form.Marker) error {
	changed := false

	if m.MarkerInvalid != f.MarkerInvalid {
		m.MarkerInvalid = f.MarkerInvalid
		changed = true
	}

	if !m.MarkerInvalid && m.Score < 100 {
		m.Score = 100
		changed = true
	}

	if f.SubjectSrc == SrcManual && strings.TrimSpace(f.MarkerName) != "" {
		m.SubjectSrc = SrcManual
		m.MarkerName = txt.Title(txt.Clip(f.MarkerName, txt.ClipDefault))

		if err := m.SyncSubject(true); err != nil {
			return err
		}

		changed = true
	}

	if changed {
		return m.Save()
	}

	return nil
}

// HasFace tests if the marker already has the best matching face.
func (m *Marker) HasFace(f *Face, dist float64) bool {
	if m.FaceID == "" {
		return false
	} else if f == nil {
		return m.FaceID != ""
	} else if m.FaceID == f.ID {
		return m.FaceID != ""
	} else if m.FaceDist < 0 {
		return false
	} else if dist < 0 {
		return true
	}

	return m.FaceDist <= dist
}

// SetFace sets a new face for this marker.
func (m *Marker) SetFace(f *Face, dist float64) (updated bool, err error) {
	if f == nil {
		return false, fmt.Errorf("face is nil")
	}

	if m.MarkerType != MarkerFace {
		return false, fmt.Errorf("not a face marker")
	}

	// Any reason we don't want to set a new face for this marker?
	if m.SubjectSrc != SrcManual || f.SubjectUID == "" || m.SubjectUID == "" || f.SubjectUID == m.SubjectUID {
		// Don't skip if subject wasn't set manually, or subjects match.
	} else if reported, err := f.ReportCollision(m.Embeddings()); err != nil {
		return false, err
	} else if reported {
		log.Infof("faces: marker %d (subject %s) collision with %s (subject %s), source %s", m.ID, m.SubjectUID, f.ID, f.SubjectUID, m.SubjectSrc)
		return false, nil
	} else {
		return false, nil
	}

	// Update face with known subject from marker?
	if f.SubjectUID != "" || m.SubjectUID == "" {
		// Don't update if face has a known subject, or marker subject is unknown.
	} else if err := f.Update("SubjectUID", m.SubjectUID); err != nil {
		return false, err
	}

	// Skip update if the same face is already set.
	if m.SubjectUID == f.SubjectUID && m.FaceID == f.ID {
		// Update matching timestamp.
		m.MatchedAt = TimePointer()
		return false, m.Updates(Values{"MatchedAt": m.MatchedAt})
	}

	// Remember current values for comparison.
	faceID := m.FaceID
	subjectUID := m.SubjectUID
	SubjectSrc := m.SubjectSrc

	m.FaceID = f.ID
	m.FaceDist = dist

	if m.FaceDist < 0 {
		faceEmbedding := f.Embedding()

		// Calculate smallest distance to embeddings.
		for _, e := range m.Embeddings() {
			if len(e) != len(faceEmbedding) {
				continue
			}

			if d := clusters.EuclideanDistance(e, faceEmbedding); d < m.FaceDist || m.FaceDist < 0 {
				m.FaceDist = d
			}
		}
	}

	if f.SubjectUID != "" {
		m.SubjectUID = f.SubjectUID
	}

	if err := m.SyncSubject(false); err != nil {
		return false, err
	}

	// Update face subject?
	if m.SubjectUID == "" || f.SubjectUID != m.SubjectUID {
		// Not needed.
	} else if err := f.Update("SubjectUID", m.SubjectUID); err != nil {
		return false, err
	}

	updated = m.FaceID != faceID || m.SubjectUID != subjectUID || m.SubjectSrc != SubjectSrc

	// Update matching timestamp.
	m.MatchedAt = TimePointer()

	return updated, m.Updates(Values{"FaceID": m.FaceID, "FaceDist": m.FaceDist, "SubjectUID": m.SubjectUID, "SubjectSrc": m.SubjectSrc, "MatchedAt": m.MatchedAt})
}

// SyncSubject maintains the marker subject relationship.
func (m *Marker) SyncSubject(updateRelated bool) error {
	// Face marker? If not, return.
	if m.MarkerType != MarkerFace {
		return nil
	}

	subj := m.GetSubject()

	if subj == nil {
		return nil
	}

	// Update subject with marker name?
	if m.MarkerName == "" || subj.SubjectName == m.MarkerName || (subj.SubjectName != "" && m.SubjectSrc != SrcManual) {
		// Do nothing.
	} else if err := subj.UpdateName(m.MarkerName); err != nil {
		return err
	}

	// Create known face for subject?
	if m.FaceID != "" || m.SubjectSrc != SrcManual {
		// Do nothing.
	} else if f := m.GetFace(); f != nil {
		m.FaceID = f.ID
	}

	// Update related markers?
	if m.FaceID == "" || m.SubjectUID == "" {
		// Do nothing.
	} else if err := Db().Model(&Face{}).Where("id = ? AND subject_uid = ''", m.FaceID).Update("SubjectUID", m.SubjectUID).Error; err != nil {
		return fmt.Errorf("%s (update known face)", err)
	} else if !updateRelated {
		return nil
	} else if err := Db().Model(&Marker{}).
		Where("id <> ?", m.ID).
		Where("face_id = ?", m.FaceID).
		Where("subject_src = ?", SrcAuto).
		Where("subject_uid <> ?", m.SubjectUID).
		Updates(Values{"SubjectUID": m.SubjectUID, "SubjectSrc": SrcAuto}).Error; err != nil {
		return fmt.Errorf("%s (update related markers)", err)
	} else {
		log.Debugf("marker: matched %s with %s", subj.SubjectName, m.FaceID)
	}

	return nil
}

// Save updates the existing or inserts a new row.
func (m *Marker) Save() error {
	if m.X == 0 || m.Y == 0 || m.X > 1 || m.Y > 1 || m.X < -1 || m.Y < -1 {
		return fmt.Errorf("marker: invalid position")
	}

	return Db().Save(m).Error
}

// Create inserts a new row to the database.
func (m *Marker) Create() error {
	if m.X == 0 || m.Y == 0 || m.X > 1 || m.Y > 1 || m.X < -1 || m.Y < -1 {
		return fmt.Errorf("marker: invalid position")
	}

	return Db().Create(m).Error
}

// Embeddings returns parsed marker embeddings.
func (m *Marker) Embeddings() Embeddings {
	if len(m.EmbeddingsJSON) == 0 {
		return Embeddings{}
	} else if len(m.embeddings) > 0 {
		return m.embeddings
	} else if err := json.Unmarshal(m.EmbeddingsJSON, &m.embeddings); err != nil {
		log.Errorf("failed parsing marker embeddings json: %s", err)
	}

	return m.embeddings
}

// GetSubject returns a subject entity if possible.
func (m *Marker) GetSubject() (subj *Subject) {
	if m.Subject != nil {
		return m.Subject
	}

	if m.SubjectUID == "" && m.MarkerName != "" {
		if subj = NewSubject(m.MarkerName, SubjectPerson, SrcMarker); subj == nil {
			return nil
		} else if subj = FirstOrCreateSubject(subj); subj == nil {
			log.Debugf("marker: invalid subject %s", txt.Quote(m.MarkerName))
			return nil
		}

		m.SubjectUID = subj.SubjectUID
		m.SubjectSrc = SrcManual

		return subj
	}

	m.Subject = FindSubject(m.SubjectUID)

	return m.Subject
}

// ClearSubject removes an existing subject association, and reports a collision.
func (m *Marker) ClearSubject(src string) error {
	if m.Face == nil {
		m.Face = FindFace(m.FaceID)
	}

	if m.Face == nil {
		// Do nothing
	} else if reported, err := m.Face.ReportCollision(m.Embeddings()); err != nil {
		return err
	} else if err := m.Updates(Values{"MarkerName": "", "FaceID": "", "FaceDist": -1.0, "SubjectUID": "", "SubjectSrc": src}); err != nil {
		return err
	} else if reported {
		log.Debugf("faces: collision with %s", m.Face.ID)
	}

	m.Face = nil

	m.MarkerName = ""
	m.FaceID = ""
	m.FaceDist = -1.0
	m.SubjectUID = ""
	m.SubjectSrc = src

	return nil
}

// GetFace returns a matching face entity if possible.
func (m *Marker) GetFace() (f *Face) {
	if m.Face != nil {
		return m.Face
	}

	// Add face if size
	if m.FaceID == "" && m.SubjectSrc == SrcManual {
		if m.Size < face.ClusterMinSize || m.Score < face.ClusterMinScore {
			log.Debugf("faces: skipped adding face for low-quality marker %d, size %d, score %d", m.ID, m.Size, m.Score)
			return nil
		} else if f = NewFace(m.SubjectUID, SrcManual, m.Embeddings()); f == nil {
			return nil
		} else if err := f.Create(); err != nil {
			return nil
		} else if err := f.MatchMarkers(Faceless); err != nil {
			log.Errorf("faces: %s (match markers)", err)
		}

		m.Face = f
		m.FaceID = f.ID
	} else {
		m.Face = FindFace(m.FaceID)
	}

	return m.Face
}

// ClearFace removes an existing face association.
func (m *Marker) ClearFace() (updated bool, err error) {
	if m.FaceID == "" {
		return false, m.Matched()
	}

	updated = true

	// Remove face references.
	m.Face = nil
	m.FaceID = ""
	m.MatchedAt = TimePointer()

	// Remove subject if set automatically.
	if m.SubjectSrc == SrcAuto {
		m.SubjectUID = ""
		err = m.Updates(Values{"FaceID": "", "FaceDist": -1.0, "SubjectUID": "", "MatchedAt": m.MatchedAt})
	} else {
		err = m.Updates(Values{"FaceID": "", "FaceDist": -1.0, "MatchedAt": m.MatchedAt})
	}

	return updated, err
}

// Matched updates the match timestamp.
func (m *Marker) Matched() error {
	m.MatchedAt = TimePointer()
	return UnscopedDb().Model(m).UpdateColumns(Values{"MatchedAt": m.MatchedAt}).Error
}

// FindMarker returns an existing row if exists.
func FindMarker(id uint) *Marker {
	result := Marker{}

	if err := Db().Where("id = ?", id).First(&result).Error; err == nil {
		return &result
	}

	return nil
}

// UpdateOrCreateMarker updates a marker in the database or creates a new one if needed.
func UpdateOrCreateMarker(m *Marker) (*Marker, error) {
	const d = 0.07

	result := Marker{}

	if m.ID > 0 {
		err := m.Save()
		log.Debugf("faces: saved marker %d for file %d", m.ID, m.FileID)
		return m, err
	} else if err := Db().Where(`file_id = ? AND x > ? AND x < ? AND y > ? AND y < ?`,
		m.FileID, m.X-d, m.X+d, m.Y-d, m.Y+d).First(&result).Error; err == nil {

		if SrcPriority[m.MarkerSrc] < SrcPriority[result.MarkerSrc] {
			// Ignore.
			return &result, nil
		}

		err := result.Updates(map[string]interface{}{
			"X":              m.X,
			"Y":              m.Y,
			"W":              m.W,
			"H":              m.H,
			"Score":          m.Score,
			"LandmarksJSON":  m.LandmarksJSON,
			"EmbeddingsJSON": m.EmbeddingsJSON,
			"SubjectUID":     m.SubjectUID,
		})

		log.Debugf("faces: updated existing marker %d for file %d", result.ID, result.FileID)

		return &result, err
	} else if err := m.Create(); err != nil {
		log.Debugf("faces: added marker %d for file %d", m.ID, m.FileID)
		return m, err
	}

	return m, nil
}
