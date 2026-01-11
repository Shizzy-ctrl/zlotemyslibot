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

	rand.Seed(time.Now().UnixNano())

	loadConfig()

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal("Błąd tworzenia sesji:", err)
	}

	dg.AddHandler(messageCreate)
	dg.Identify.Intents = discordgo.IntentsGuildMessages

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
!gem [URL] - Pobierz wykres ze Stooq
!pomoc - Pokaż tę pomoc`
		s.ChannelMessageSend(m.ChannelID, help)
	}
}

func handleGemCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	urlStr := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m.Content), "!gem"))
	if urlStr == "" {
		urlStr = "https://stooq.pl/q/?s=eimi.uk&d=20260105&c=1y&t=l&a=lg&r=cndx.uk+cbu0.uk+ib01.uk"
	}

	s.ChannelMessageSend(m.ChannelID, "⏳ Pobieram wykres...")

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
	// Parsuj główny URL
	parsedURL, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("nieprawidłowy URL: %w", err)
	}

	// Wyciągnij parametry z URL
	query := parsedURL.Query()
	symbol := query.Get("s")
	if symbol == "" {
		return nil, errors.New("brak symbolu (parametr 's') w URL")
	}

	// Buduj bezpośredni URL do wykresu PNG
	chartURL := buildChartURL(query)

	log.Printf("Próba pobrania wykresu z: %s", chartURL)

	// Pobierz PNG z kilkoma próbami
	for attempt := 1; attempt <= 3; attempt++ {
		pngBytes, err := fetchStooqPNG(pageURL, chartURL)
		if err == nil && isPNG(pngBytes) {
			return pngBytes, nil
		}
		log.Printf("Próba %d/3 nieudana: %v", attempt, err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	// Jeśli bezpośrednie pobieranie nie działa, spróbuj ze scrapowania HTML
	log.Println("Bezpośrednie pobieranie nie powiodło się. Próba scrapowania HTML...")
	return scrapeFromHTML(pageURL)
}

func buildChartURL(query url.Values) string {
	// Podstawowe parametry dla wykresu PNG
	params := url.Values{}

	// Symbol
	if s := query.Get("s"); s != "" {
		params.Set("s", s)
	}

	// Data
	if d := query.Get("d"); d != "" {
		params.Set("d", d)
	} else {
		params.Set("d", time.Now().Format("20060102"))
	}

	// Okres wykresu
	if c := query.Get("c"); c != "" {
		params.Set("c", c)
	} else {
		params.Set("c", "1y")
	}

	// Typ wykresu
	if t := query.Get("t"); t != "" {
		params.Set("t", t)
	} else {
		params.Set("t", "l")
	}

	// Analiza
	if a := query.Get("a"); a != "" {
		params.Set("a", a)
	}

	// Porównania
	if r := query.Get("r"); r != "" {
		params.Set("r", r)
	}

	// Dodatkowe parametry dla lepszej jakości
	params.Set("g", "1") // Siatka

	return "https://stooq.pl/q/c/?" + params.Encode()
}

func scrapeFromHTML(pageURL string) ([]byte, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}

	// Symuluj przeglądarkę
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pl,en-US;q=0.7,en;q=0.3")
	req.Header.Set("Cookie", "privacy=1")
	req.Header.Set("Referer", "https://stooq.pl/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	// Szukaj obrazka wykresu
	var imgSrc string

	// Metoda 1: Główny wykres
	doc.Find("div#aqi_mc img, div#chart img, img[id*='chart']").Each(func(_ int, s *goquery.Selection) {
		if src, exists := s.Attr("src"); exists && src != "" {
			imgSrc = src
			return
		}
		if src, exists := s.Attr("src2"); exists && src != "" {
			imgSrc = src
		}
	})

	// Metoda 2: Szukaj wszystkich obrazków z data:image/png
	if imgSrc == "" {
		doc.Find("img").Each(func(_ int, s *goquery.Selection) {
			if src, exists := s.Attr("src"); exists && strings.HasPrefix(src, "data:image/png;base64,") {
				imgSrc = src
				return
			}
		})
	}

	if imgSrc == "" {
		return nil, errors.New("nie znaleziono obrazka wykresu na stronie")
	}

	// Jeśli to base64, dekoduj
	if strings.HasPrefix(imgSrc, "data:image/png;base64,") {
		b64Data := strings.TrimPrefix(imgSrc, "data:image/png;base64,")
		pngBytes, err := base64.StdEncoding.DecodeString(b64Data)
		if err != nil {
			return nil, fmt.Errorf("błąd dekodowania base64: %w", err)
		}
		if !isPNG(pngBytes) {
			return nil, errors.New("zdekodowane dane nie są PNG")
		}
		return pngBytes, nil
	}

	// Jeśli to URL względny lub bezwzględny
	baseURL, _ := url.Parse(pageURL)
	imgURL, err := baseURL.Parse(imgSrc)
	if err != nil {
		return nil, fmt.Errorf("nieprawidłowy URL obrazka: %w", err)
	}

	return fetchStooqPNG(pageURL, imgURL.String())
}

func fetchStooqPNG(refererURL, imgURL string) ([]byte, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	req, err := http.NewRequest(http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, err
	}

	// Ważne nagłówki dla Stooq
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pl,en-US;q=0.7,en;q=0.3")
	req.Header.Set("Referer", refererURL)
	req.Header.Set("Cookie", "privacy=1")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyPreview, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("HTTP %d, odpowiedź: %s", resp.StatusCode, string(bodyPreview))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if !isPNG(data) {
		ct := resp.Header.Get("Content-Type")
		preview := string(data)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("odpowiedź nie jest PNG (Content-Type: %s, %d bajtów)", ct, len(data))
	}

	return data, nil
}

func isPNG(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	return b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4e && b[3] == 0x47 &&
		b[4] == 0x0d && b[5] == 0x0a && b[6] == 0x1a && b[7] == 0x0a
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
			sendDailyQuote(s, config.ChannelID)
		}
	})
	if err != nil {
		log.Fatal("Cron AddFunc błąd:", err)
	}

	fmt.Println("✅ Cron działa - 9:00 CET codziennie!")
	c.Start()
}

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

		if _, err := s.ChannelMessageSend(channelID, msg.String()); err != nil {
			log.Println("Błąd wysyłania listy:", err)
			return
		}

		time.Sleep(1000 * time.Millisecond)
	}
}
