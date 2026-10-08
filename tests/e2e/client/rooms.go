package client

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/textproto"
)

// Scene is the room's scene context.
type Scene struct {
	Topic       string `json:"topic"`
	Description string `json:"description"`
	Genre       string `json:"genre"`
}

// Character is one of the two characters as submitted.
type Character struct {
	Name  string `json:"name"`
	Brief string `json:"brief"`
}

// RoomFields is the JSON part of a room submission.
type RoomFields struct {
	Scene      Scene     `json:"scene"`
	CharacterA Character `json:"characterA"`
	CharacterB Character `json:"characterB"`
}

// File is an uploaded file.
type File struct {
	Name        string
	ContentType string
	Content     []byte
}

// RoomInput is everything the setup form submits. A nil portrait is left out
// of the request, to test the missing-portrait case.
type RoomInput struct {
	Fields    RoomFields
	PortraitA *File
	PortraitB *File
}

// CharacterView is a character as the server returns it.
type CharacterView struct {
	Name        string `json:"name"`
	Brief       string `json:"brief"`
	PortraitURL string `json:"portraitUrl"`
}

// RoomView is a room as the server returns it to its director.
type RoomView struct {
	RoomID     string        `json:"roomId"`
	Scene      Scene         `json:"scene"`
	CharacterA CharacterView `json:"characterA"`
	CharacterB CharacterView `json:"characterB"`
	// ShareLink and AudienceCount are present only in the director's view.
	ShareLink     string `json:"shareLink"`
	AudienceCount int    `json:"audienceCount"`
}

// RoomSummary is one entry in the director's room list.
type RoomSummary struct {
	RoomID string `json:"roomId"`
	Topic  string `json:"topic"`
}

// CreateRoom submits the setup form as one multipart request.
func (d *DirectorClient) CreateRoom(ctx context.Context, in RoomInput) (*Response, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	fields, err := json.Marshal(in.Fields)
	if err != nil {
		return nil, err
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="room"`)
	header.Set("Content-Type", "application/json")
	part, err := form.CreatePart(header)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(fields); err != nil {
		return nil, err
	}

	for name, file := range map[string]*File{"portraitA": in.PortraitA, "portraitB": in.PortraitB} {
		if file == nil {
			continue
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+name+`"; filename="`+file.Name+`"`)
		header.Set("Content-Type", file.ContentType)
		part, err := form.CreatePart(header)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(file.Content); err != nil {
			return nil, err
		}
	}
	if err := form.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/api/v1/rooms", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	return d.do(req)
}

// GetRoom reads one room, as whoever this browser is: its director or a
// member of its audience.
func (c *Client) GetRoom(ctx context.Context, roomID string) (*Response, error) {
	return c.Get(ctx, "/api/v1/rooms/"+roomID)
}

// ListRooms reads the director's own rooms.
func (d *DirectorClient) ListRooms(ctx context.Context) (*Response, error) {
	return d.Get(ctx, "/api/v1/rooms")
}

// FetchPortrait reads a character's portrait; slot is "a" or "b".
func (c *Client) FetchPortrait(ctx context.Context, roomID, slot string) (*Response, error) {
	return c.Get(ctx, "/api/v1/rooms/"+roomID+"/portraits/"+slot)
}
