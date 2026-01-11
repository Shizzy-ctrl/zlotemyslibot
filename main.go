package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/bwmarrin/discordgo"
	"github.com/robfig/cron/v3"
)

type Config struct {
	Quotes    []string `json:"quotes"`
	ChannelID string   `json:"channel_id"`
}

var (
	config     Config
	configFile = "config.json"
)

func main() {
	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("Brak tokena Discord! Ustaw zmienną DISCORD_TOKEN")
	}

	rand.Seed(time.Now().UnixNano()) // ✅ Losowe cytaty

	loadConfig()

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal("Błąd tworzenia sesji:", err)
	}

	dg.AddHandler(messageCreate)
	dg.Identify.Intents = discordgo.IntentsGuildMessages

	// 🚀 CRON SCHEDULER zamiast tickera
	go startCronScheduler(dg)

	err = dg.Open()
	if err != nil {
		log.Fatal("Błąd otwierania połączenia:", err)
	}
	defer dg.Close()

	fmt.Println("Bot działa! Codzienne cytaty o 9:00 CET. Naciśnij CTRL+C aby zakończyć.")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

func loadConfig() {
	data, err := os.ReadFile(configFile)
	if err != nil {
		config = Config{
			Quotes: []string{
				"Wytrwałość to klucz do sukcesu.",
				"Każdy dzień to nowa szansa.",
				"Wierz w siebie i swoje możliwości.",
			},
			ChannelID: "",
		}
		saveConfig()
		return
	}
	json.Unmarshal(data, &config)
}

func saveConfig() {
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(configFile, data, 0o644)
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	content := strings.TrimSpace(m.Content)

	if content == "!zlotamysl" || content == "!zm" {
		sendRandomQuote(s, m.ChannelID)
	} else if strings.HasPrefix(content, "!gem") {
		handleGemCommand(s, m)
	} else if strings.HasPrefix(content, "!dodaj ") {
		quote := strings.TrimPrefix(content, "!dodaj ")
		config.Quotes = append(config.Quotes, quote)
		saveConfig()
		s.ChannelMessageSend(m.ChannelID, "✅ Dodano nową złotą myśl!")
	} else if strings.HasPrefix(content, "!usun ") {
		numStr := strings.TrimPrefix(content, "!usun ")
		var num int
		fmt.Sscanf(numStr, "%d", &num)
		if num > 0 && num <= len(config.Quotes) {
			config.Quotes = append(config.Quotes[:num-1], config.Quotes[num:]...)
			saveConfig()
			s.ChannelMessageSend(m.ChannelID, "✅ Usunięto złotą myśl!")
		} else {
			s.ChannelMessageSend(m.ChannelID, "❌ Nieprawidłowy numer!")
		}
	} else if content == "!lista" {
		sendPaginatedList(s, m.ChannelID)
	} else if strings.HasPrefix(content, "!kanal ") {
		channelID := strings.TrimPrefix(content, "!kanal ")
		config.ChannelID = channelID
		saveConfig()
		s.ChannelMessageSend(m.ChannelID, "✅ Ustawiono kanał dla codziennych myśli!")
	} else if content == "!pomoc" {
		help := `**🌟 Złote Myśli Bot - Komendy:**

!zlotamysl lub !zm - Wyświetl losową złotą myśl
!dodaj <tekst> - Dodaj nową złotą myśl
!usun <numer> - Usuń złotą myśl (podaj numer z listy)
!lista - Pokaż wszystkie złote myśli
!kanal <ID> - Ustaw kanał dla codziennych myśli o 9:00
!pomoc - Pokaż tę pomoc`
		s.ChannelMessageSend(m.ChannelID, help)
	}
}

func handleGemCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	urlStr := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m.Content), "!gem"))
	if urlStr == "" {
		urlStr = "https://stooq.pl/q/?s=eimi.uk&d=20260105&c=1y&t=l&a=lg&r=cndx.uk+cbu0.uk+ib01.uk"
	}

	pngBytes, err := scrapeStooqChartPNG(urlStr)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Nie udało się pobrać wykresu: "+err.Error())
		return
	}

	_, err = s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Files: []*discordgo.File{
			{Name: "gem.png", ContentType: "image/png", Reader: bytes.NewReader(pngBytes)},
		},
	})
	if err != nil {
		log.Println("Błąd wysyłania pliku na Discord:", err)
	}
}

func scrapeStooqChartPNG(pageURL string) ([]byte, error) {
	baseURL, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("nieprawidłowy URL: %w", err)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, baseURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	img := doc.Find("div#aqi_mc img").First()
	if img.Length() == 0 {
		return nil, errors.New("nie znaleziono obrazka (div#aqi_mc img)")
	}

	src, ok := img.Attr("src")
	if !ok || strings.TrimSpace(src) == "" {
		return nil, errors.New("obrazek nie ma atrybutu src")
	}

	const prefix = "data:image/png;base64,"
	if strings.HasPrefix(src, prefix) {
		raw := strings.TrimPrefix(src, prefix)
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("błąd dekodowania base64: %w", err)
		}
		return b, nil
	}

	imgURL, err := baseURL.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("nieprawidłowy URL obrazka: %w", err)
	}

	imgReq, err := http.NewRequest(http.MethodGet, imgURL.String(), nil)
	if err != nil {
		return nil, err
	}
	imgReq.Header.Set("User-Agent", "Mozilla/5.0")

	imgResp, err := client.Do(imgReq)
	if err != nil {
		return nil, err
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode < 200 || imgResp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d (obrazek)", imgResp.StatusCode)
	}

	data, err := io.ReadAll(imgResp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func sendRandomQuote(s *discordgo.Session, channelID string) {
	if len(config.Quotes) == 0 {
		s.ChannelMessageSend(channelID, "Brak złotych myśli! Dodaj je komendą !dodaj")
		return
	}
	quote := config.Quotes[rand.Intn(len(config.Quotes))]
	s.ChannelMessageSend(channelID, fmt.Sprintf("✨ **Złota Myśl:** ✨\n\n*%s*", quote))
}

func startCronScheduler(s *discordgo.Session) {
	loc, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		log.Fatal("Location error:", err)
	}

	c := cron.New(cron.WithLocation(loc))

	_, err = c.AddFunc("0 9 * * ?", func() {
		fmt.Println("🕐 CRON 9:00 CET!")
		if config.ChannelID != "" {
			// ZMIENIONO: "Złota myśl dnia" zamiast zwykłej złotej myśli
			sendDailyQuote(s, config.ChannelID)
		}
	})
	if err != nil {
		log.Fatal("Cron AddFunc błąd:", err)
	}

	fmt.Println("✅ Cron działa - 9:00 CET codziennie!")
	c.Start()
}

// NOWA FUNKCJA dla zaplanowanej złotej myśli dnia
func sendDailyQuote(s *discordgo.Session, channelID string) {
	if len(config.Quotes) == 0 {
		s.ChannelMessageSend(channelID, "Brak złotych myśli! Dodaj je komendą !dodaj")
		return
	}
	quote := config.Quotes[rand.Intn(len(config.Quotes))]
	s.ChannelMessageSend(channelID, fmt.Sprintf("🌅 **Złota myśl dnia** 🌅\n\n*%s*", quote))
}

func sendPaginatedList(s *discordgo.Session, channelID string) {
	if len(config.Quotes) == 0 {
		s.ChannelMessageSend(channelID, "Brak złotych myśli!")
		return
	}

	const maxChars = 1800
	const maxQuotesPerPage = 12

	for i := 0; i < len(config.Quotes); i += maxQuotesPerPage {
		end := i + maxQuotesPerPage
		if end > len(config.Quotes) {
			end = len(config.Quotes)
		}

		var msg strings.Builder
		msg.WriteString(fmt.Sprintf("**📜 Złote Myśli (%d-%d/%d):**\n\n", i+1, end, len(config.Quotes)))

		pageChars := 50
		for j := i; j < end; j++ {
			quoteNum := fmt.Sprintf("%d. ", j+1)
			quotePreview := config.Quotes[j]

			if len(quotePreview) > 100 {
				quotePreview = quotePreview[:97] + "..."
			}

			line := quoteNum + quotePreview + "\n"
			if pageChars+len(line) > maxChars {
				break
			}

			msg.WriteString(line)
			pageChars += len(line)
		}

		// POPRAWIONE: _ dla message, err dla błędu
		if _, err := s.ChannelMessageSend(channelID, msg.String()); err != nil {
			log.Println("Błąd wysyłania listy:", err)
			return
		}

		time.Sleep(1000 * time.Millisecond)
	}
}
