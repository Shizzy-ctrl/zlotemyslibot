package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
		log.Fatal("Location error:", err) // Teraz pokaże konkretny błąd
	}

	c := cron.New(cron.WithLocation(loc))

	_, err = c.AddFunc("0 9 * * ?", func() {
		fmt.Println("🕐 CRON 9:00 CET!")
		if config.ChannelID != "" {
			sendRandomQuote(s, config.ChannelID)
		}
	})
	if err != nil {
		log.Fatal("Cron AddFunc błąd:", err)
	}

	fmt.Println("✅ Cron działa - 9:00 CET codziennie!")
	c.Start()
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
