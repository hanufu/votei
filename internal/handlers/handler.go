package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Hanufu/votei/internal/config"
	"github.com/Hanufu/votei/internal/database"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type Vote struct {
	IPAddress       string    `json:"ip_address"`
	UserAgent       string    `json:"user_agent"`
	CookieID        string    `json:"cookie_id"`
	Timestamp       time.Time `json:"timestamp"`
	Referer         string    `json:"referer"`
	Language        string    `json:"language"`
	Browser         string    `json:"browser"`
	CandidateNumber int       `json:"candidate_number"`
	Latitude        string    `json:"latitude"`
	Longitude       string    `json:"longitude"`
}

func ServeFile(fileName string) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.File(config.StaticPath + fileName)
	}
}

func GetUniqueIdentifier(c echo.Context) string {
	ip := c.Request().Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = c.RealIP()
	}
	userAgent := c.Request().Header.Get("User-Agent")
	return ip + "-" + userAgent
}

func GenerateCookieID(c echo.Context) string {
	cookie, err := c.Cookie("voter_id")
	if err != nil {
		if err == http.ErrNoCookie {
			newID := uuid.New().String()
			c.SetCookie(&http.Cookie{
				Name:    "voter_id",
				Value:   newID,
				Path:    "/",
				Expires: time.Now().Add(40 * 24 * time.Hour),
			})
			return newID
		}
		log.Printf("Erro ao obter cookie: %v", err)
		return ""
	}
	return cookie.Value
}

func HasVoted(ip string, userAgent string, cookieID string) bool {
	var count int
	query := `
		SELECT COUNT(*) 
		FROM votes 
		WHERE ip_address = ? 
			OR user_agent = ? 
			OR cookie_id = ?`
	if err := database.DB.QueryRow(query, ip, userAgent, cookieID).Scan(&count); err != nil {
		fmt.Println("Erro ao verificar votos:", err)
		return false
	}
	return count > 0
}

func HasVotedRecently(ip string, userAgent string, cookieID string) bool {
	var count int
	query := `
		SELECT COUNT(*) 
		FROM votes 
		WHERE (ip_address = ? OR user_agent = ? OR cookie_id = ?) 
		  AND timestamp > ?`
	oneDayAgo := time.Now().Add(-24 * time.Hour)
	if err := database.DB.QueryRow(query, ip, userAgent, cookieID, oneDayAgo).Scan(&count); err != nil {
		fmt.Println("Erro ao verificar votos recentes:", err)
		return false
	}
	return count > 0
}

func RegisterVote(vote Vote) {
	database.DBLock.Lock()
	defer database.DBLock.Unlock()

	_, err := database.DB.Exec("INSERT INTO votes (ip_address, user_agent, cookie_id, timestamp, referer, language, browser, candidate_number, latitude, longitude) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		vote.IPAddress, vote.UserAgent, vote.CookieID, vote.Timestamp, vote.Referer, vote.Language, vote.Browser, vote.CandidateNumber, vote.Latitude, vote.Longitude)
	if err != nil {
		fmt.Println("Erro ao registrar voto:", err)
		return
	}

	database.VoteCounts.Lock()
	defer database.VoteCounts.Unlock()
	database.VoteCounts.Counts[vote.CandidateNumber]++
}

func LogVote(vote Vote) {
	fmt.Printf("Voto registrado:\n")
	fmt.Printf("IP: %s\n", vote.IPAddress)
	fmt.Printf("User-Agent: %s\n", vote.UserAgent)
	fmt.Printf("Cookie ID: %s\n", vote.CookieID)
	fmt.Printf("Timestamp: %s\n", vote.Timestamp.Format(time.RFC3339))
	fmt.Printf("Referer: %s\n", vote.Referer)
	fmt.Printf("Language: %s\n", vote.Language)
	fmt.Printf("Browser: %s\n", vote.Browser)
	fmt.Printf("Número do Candidato: %d\n", vote.CandidateNumber)
	fmt.Printf("Latitude: %s\n", vote.Latitude)
	fmt.Printf("Longitude: %s\n", vote.Longitude)
}

func ResultHandler(c echo.Context) error {
	message := c.QueryParam("message")

	database.VoteCounts.RLock()
	defer database.VoteCounts.RUnlock()

	data := struct {
		Message    string
		BlankVotes int
		Votes45    int
		Votes13    int
	}{
		Message:    message,
		BlankVotes: database.VoteCounts.Counts[0],
		Votes45:    database.VoteCounts.Counts[45],
		Votes13:    database.VoteCounts.Counts[13],
	}

	err := config.ResultTemplate.Execute(c.Response().Writer, data)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"message": "Internal Server Error"})
	}

	return nil
}

func isValidCoordinate(coord string) bool {
	_, err := strconv.ParseFloat(coord, 64)
	return err == nil
}

func VoteHandler(c echo.Context) error {
	cookieID := GenerateCookieID(c)
	ip := c.RealIP()
	userAgent := c.Request().Header.Get("User-Agent")
	referer := c.Request().Header.Get("Referer")
	acceptLanguage := c.Request().Header.Get("Accept-Language")

	var browser string
	if userAgent != "" {
		if strings.Contains(userAgent, "Firefox") {
			browser = "Firefox"
		} else if strings.Contains(userAgent, "Chrome") {
			browser = "Chrome"
		} else if strings.Contains(userAgent, "Safari") {
			browser = "Safari"
		} else {
			browser = "Other"
		}
	}

	candidateNumber := c.FormValue("candidate_number")
	latitude := c.FormValue("latitude")
	longitude := c.FormValue("longitude")

	if !isValidCoordinate(latitude) || !isValidCoordinate(longitude) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Coordenadas inválidas."})
	}

	var candidateNumberInt int
	if candidateNumber == "00" {
		candidateNumberInt = 0
	} else {
		var err error
		candidateNumberInt, err = strconv.Atoi(candidateNumber)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Número do candidato inválido."})
		}
	}

	if HasVoted(ip, userAgent, cookieID) {
		return c.Redirect(http.StatusSeeOther, "/result?message=Você já votou antes! Seu voto já foi registrado e não será computado novamente.")
	}

	if HasVotedRecently(ip, userAgent, cookieID) {
		return c.Redirect(http.StatusSeeOther, "/result?message=Você já votou recentemente! Espere 24 horas antes de votar novamente.")
	}

	vote := Vote{
		IPAddress:       ip,
		UserAgent:       userAgent,
		CookieID:        cookieID,
		Timestamp:       time.Now(),
		Referer:         referer,
		Language:        acceptLanguage,
		Browser:         browser,
		CandidateNumber: candidateNumberInt,
		Latitude:        latitude,
		Longitude:       longitude,
	}

	RegisterVote(vote)
	LogVote(vote)

	return c.Redirect(http.StatusSeeOther, "/result")
}
