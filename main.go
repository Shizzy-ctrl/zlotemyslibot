package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/robfig/cron/v3"
	"golang.org/x/net/html"
)

type Config struct {
	Quotes    []string `json:"quotes"`
	ChannelID string   `json:"channel_id"`
}

var (
	config     Config
	configFile = "config.json"
)

func scrapeGemImage() (string, error) {
	url := "https://stooq.pl/q/?s=eimi.uk&d=20260105&c=1y&t=l&a=lg&r=cndx.uk+cbu0.uk+ib01.uk"

	// Dodajemy User-Agent aby uniknąć blokady
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("błąd tworzenia requestu: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("błąd pobierania strony: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("błąd HTTP: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("błąd odczytu body: %v", err)
	}

	// Najpierw spróbuj znaleźć div z id="aqi_mc" w surowym HTML
	bodyStr := string(body)

	// Szukamy wzorca: <div id="aqi_mc"><img src="..." src2="..."
	divPattern := `<div[^>]*id=["']aqi_mc["'][^>]*>.*?<img[^>]*src=["']([^"']+)["'][^>]*src2=["']([^"']+)["']`
	re := regexp.MustCompile(divPattern)
	matches := re.FindStringSubmatch(bodyStr)

	if len(matches) >= 3 {
		// matches[1] = src, matches[2] = src2
		src2 := matches[2]
		// Jeśli src2 jest relatywny, budujemy pełny URL
		if strings.HasPrefix(src2, "c/") {
			return "https://stooq.pl/" + src2, nil
		}
		return src2, nil
	}

	// Alternatywnie szukamy samego src2 w kontekście aqi_mc
	src2Pattern := `id=["']aqi_mc["'][^>]*>.*?src2=["']([^"']+)["']`
	re2 := regexp.MustCompile(src2Pattern)
	matches2 := re2.FindStringSubmatch(bodyStr)

	if len(matches2) >= 2 {
		src2 := matches2[1]
		if strings.HasPrefix(src2, "c/") {
			return "https://stooq.pl/" + src2, nil
		}
		return src2, nil
	}

	// Jeśli regex nie zadziałał, próbujemy parsować HTML
	doc, err := html.Parse(strings.NewReader(bodyStr))
	if err != nil {
		return "", fmt.Errorf("błąd parsowania HTML: %v", err)
	}

	var findDiv func(*html.Node) *html.Node
	findDiv = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && n.Data == "div" {
			for _, attr := range n.Attr {
				if attr.Key == "id" && attr.Val == "aqi_mc" {
					return n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if found := findDiv(c); found != nil {
				return found
			}
		}
		return nil
	}

	aqiDiv := findDiv(doc)
	if aqiDiv == nil {
		return "", fmt.Errorf("nie znaleziono div'a o id='aqi_mc'")
	}

	// Szukamy obrazka z src2
	var findImage func(*html.Node) string
	findImage = func(n *html.Node) string {
		if n.Type == html.ElementNode && n.Data == "img" {
			var src, src2 string
			for _, attr := range n.Attr {
				if attr.Key == "src2" {
					src2 = attr.Val
				} else if attr.Key == "src" {
					src = attr.Val
				}
			}
			// Preferujemy src2, jeśli nie ma to src
			if src2 != "" {
				if strings.HasPrefix(src2, "c/") {
					return "https://stooq.pl/" + src2
				}
				return src2
			}
			return src
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if imgSrc := findImage(c); imgSrc != "" {
				return imgSrc
			}
		}
		return ""
	}

	imageSrc := findImage(aqiDiv)
	if imageSrc == "" {
		return "", fmt.Errorf("nie znaleziono obrazka w div'ie")
	}

	return imageSrc, nil
}

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
	} else if content == "!gem" {
		s.ChannelMessageSend(m.ChannelID, "🔍 **Szukam obrazka GEM...**")

		imageSrc, err := scrapeGemImage()
		if err != nil {
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Błąd: %v", err))
			return
		}

		if strings.HasPrefix(imageSrc, "data:image") {
			s.ChannelMessageSend(m.ChannelID, "💎 **GEM Chart (Base64)**\n\nObrazek został znaleziony w formie base64")
		} else {
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("💎 **GEM Chart**\n\nŹródło obrazka: %s", imageSrc))
		}
	} else if content == "!pomoc" {
		help := `**🌟 Złote Myśli Bot - Komendy:**

!zlotamysl lub !zm - Wyświetl losową złotą myśl
!dodaj <tekst> - Dodaj nową złotą myśl
!usun <numer> - Usuń złotą myśl (podaj numer z listy)
!lista - Pokaż wszystkie złote myśli
!kanal <ID> - Ustaw kanał dla codziennych myśli o 9:00
!gem - Pobierz wykres GEM ze Stooq.pl
!pomoc - Pokaż tę pomoc`
		s.ChannelMessageSend(m.ChannelID, help)
	}
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
