package server

import (
	"encoding/json"
	"net/http"

	"github.com/gartnera/gofin/ent/user"
	"github.com/gartnera/gofin/internal/auth"
	"github.com/gartnera/gofin/internal/jellyfin"
	"github.com/sj14/jellyfin-go/api"
)

func (s *Server) handleAuthenticateByName(w http.ResponseWriter, r *http.Request) {
	var body api.AuthenticateUserByName
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := body.GetUsername()
	password := body.GetPw()
	if username == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	u, err := s.client.User.Query().Where(user.NameEQ(username)).Only(r.Context())
	if err != nil || !auth.CheckPassword(u.PasswordHash, password) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pairs := ParseAuthorization(r.Header.Get("Authorization"))
	if len(pairs) == 0 {
		pairs = ParseAuthorization(r.Header.Get("X-Emby-Authorization"))
	}
	if _, err := s.client.AccessToken.Create().
		SetToken(token).
		SetClient(pairs["Client"]).
		SetDevice(pairs["Device"]).
		SetDeviceID(pairs["DeviceId"]).
		SetVersion(pairs["Version"]).
		SetUser(u).
		Save(r.Context()); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	userDto := jellyfin.MapUser(u, s.serverID)

	result := api.NewAuthenticationResult()
	result.SetUser(userDto)
	result.SetAccessToken(token)
	result.SetServerId(s.serverID)

	session := api.NewSessionInfoDto()
	session.SetId(token)
	session.SetUserId(jellyfin.FormatID(u.ID))
	session.SetUserName(u.Name)
	session.SetServerId(s.serverID)
	if pairs["Client"] != "" {
		session.SetClient(pairs["Client"])
	}
	if pairs["Device"] != "" {
		session.SetDeviceName(pairs["Device"])
	}
	result.SetSessionInfo(*session)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCurrentUser(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	dto := jellyfin.MapUser(u, s.serverID)
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.client.User.Query().All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dtos := make([]api.UserDto, 0, len(users))
	for _, u := range users {
		dtos = append(dtos, jellyfin.MapUser(u, s.serverID))
	}
	writeJSON(w, http.StatusOK, dtos)
}

func (s *Server) handleUserByID(w http.ResponseWriter, r *http.Request) {
	id, err := jellyfin.ParseID(r.PathValue("userId"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	u, err := s.client.User.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	dto := jellyfin.MapUser(u, s.serverID)
	writeJSON(w, http.StatusOK, dto)
}
