package handlers

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
		WHERE (ip_address = $1 AND user_agent = $2) 
			OR (ip_address = $3 AND cookie_id = $4) 
			OR (user_agent = $5 AND cookie_id = $6)`

	if err := database.DB.QueryRow(query, ip, userAgent, ip, cookieID, userAgent, cookieID).Scan(&count); err != nil {
		fmt.Println("Erro ao verificar votos:", err)
		return false
	}

	return count > 0
}

func RegisterVote(vote Vote) {
	database.DBLock.Lock()
	defer database.DBLock.Unlock()

	_, err := database.DB.Exec("INSERT INTO votes (ip_address, user_agent, cookie_id, timestamp, referer, language, browser, candidate_number, latitude, longitude) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)",
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

	// Verifique se o usuário já votou independentemente das coordenadas
	if HasVoted(ip, userAgent, cookieID) {
		return c.Redirect(http.StatusSeeOther, "/result?message=Você já votou antes! Seu voto já foi registrado e não será computado novamente.")
	}

	// Adicione a verificação das coordenadas apenas se o usuário não tiver votado
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

func GetEmailHandler(c echo.Context) error {
	email := c.FormValue("email")

	filePath := "../../database/email.txt"

	dir := "../../database"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.Mkdir(dir, 0755); err != nil {
			fmt.Printf("Erro ao criar diretório: %s\n", err)
			return c.String(http.StatusInternalServerError, "Erro ao criar diretório para salvar e-mail")
		}
	}

	// Abrir ou criar o arquivo email.txt
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("Erro ao abrir o arquivo: %s\n", err)
		return c.String(http.StatusInternalServerError, "Erro ao salvar e-mail")
	}
	defer file.Close()

	// Escrever o e-mail no arquivo
	if _, err := file.WriteString(email + "\n"); err != nil {
		fmt.Printf("Erro ao escrever no arquivo: %s\n", err)
		return c.String(http.StatusInternalServerError, "Erro ao salvar e-mail")
	}

	// Retornar a mensagem de confirmação
	return c.HTML(http.StatusOK, `
		<!DOCTYPE html>
		<html lang="pt-br">
		<head>
			<meta charset="UTF-8">
			<meta name="viewport" content="width=device-width, initial-scale=1.0">
			<title>Confirmação</title>
			<style>
				* {
					margin: 0;
					padding: 0;
					box-sizing: border-box;
				}
				body {
					width: 100%;
					height: 100%;
					font-family: "Poppins", sans-serif;
					background-color: #F5F5DC;
					padding: 20px;
					display: flex;
					justify-content: center;
					flex-direction: column; 
					align-items: center;
					text-align: center;
					margin-top: 10rem;
				}
				h1 {
					font-family: "Bebas Neue", sans-serif;
					font-size: 2.5rem;
					color: #333;
					margin-bottom: 20px;
				}
				p {
					font-size: 1rem;
					color: #555;
					line-height: 1.6;
					margin-bottom: 15px;
				}
				a {
					color: #007BFF;
					text-decoration: none;
					font-weight: 500;
				}
				a:hover {
					text-decoration: underline;
				}
			</style>
		</head>
		<body>
			<h1>Obrigado!</h1>
			<p>Te notificaremos assim que o sistema voltar.</p>
			<a href="/">Voltar</a>
		</body>
		</html>
	`)
}

func AdminLoginHandler(c echo.Context) error {
	expectedUsername := "@votei!"
	expectedPassword := "Eduard00"

	inputUsername := c.FormValue("username")
	inputPassword := c.FormValue("password")

	if inputUsername == expectedUsername && inputPassword == expectedPassword {
		return c.File(config.Dashboard)
	}

	return c.Redirect(http.StatusFound, "/admin")
}

func DownloadFileHandler(c echo.Context) error {
	filename := c.Param("filename")
	filePath := filepath.Join("../../database", filename)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return echo.NewHTTPError(http.StatusNotFound, "File not found")
	}

	return c.File(filePath)
}
