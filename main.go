package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

var client *whatsmeow.Client
var container *sqlstore.Container

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		fmt.Println("Received a message!", v.Message.GetConversation())
	}
}

func panggilWa(container *sqlstore.Container) string {
	if container == nil {
		fmt.Println("Container is not initialized")
		return ""
	}

	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		fmt.Println("Failed to get first device:", err)
		return ""
	}

	clientLog := waLog.Stdout("Client", "DEBUG", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	if client.Store.ID == nil {
		fmt.Println("Client is not logged in, generating QR code...")
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			fmt.Println("Failed to connect:", err)
			return ""
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				fmt.Println("QR code generated:", evt.Code)
				png, err := qrcode.Encode(evt.Code, qrcode.Medium, 256)
				if err != nil {
					fmt.Printf("Failed to create QR code: %v", err)
					return ""
				}
				dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
				return dataURI
			} else {
				fmt.Println("Login event:", evt.Event)
			}
		}
	} else {
		fmt.Println("Client already logged in, connecting...")
		err = client.Connect()
		if err != nil {
			fmt.Println("Failed to connect:", err)
			return ""
		}
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	client.Disconnect()
	return "0"
}

func Dbase() {
	var err error
	connectionString := "user= password= dbname= host=localhost sslmode=disable"
	dbLog := waLog.Stdout("Database", "DEBUG", true)
	container, err = sqlstore.New("postgres", connectionString, dbLog)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
}

func GetDb() *sqlstore.Container {
	return container
}

func Scan(w http.ResponseWriter, r *http.Request) {
	if client == nil {
		hasil := panggilWa(GetDb())
		if hasil == "" {
			http.Error(w, "Failed to generate QR code", http.StatusInternalServerError)
			return
		}
		tmpl := template.Must(template.ParseFiles("scanner.html"))
		w.Header().Set("Content-Type", "text/html")
		err := tmpl.Execute(w, template.URL(hasil))
		if err != nil {
			http.Error(w, "Unable to render template", http.StatusInternalServerError)
		}
	} else {
		http.Error(w, "Client already logged in", http.StatusOK)
	}
}

type User struct {
	MobileNo    string `json:"mobile_no"`
	TextMessage string `json:"text_message"`
}

func Kirim(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	var data User
	err = json.Unmarshal(body, &data)
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	targetJID, _ := types.ParseJID(data.MobileNo + "@s.whatsapp.net")
	msg := waE2E.Message{
		Conversation: proto.String(data.TextMessage),
	}

	if client != nil {
		sendMessage, err := client.SendMessage(context.Background(), targetJID, &msg)
		if err != nil {
			response := map[string]interface{}{
				"meta": map[string]interface{}{"status": "error", "message": err.Error()},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
			return
		}
		response := struct {
			Meta map[string]interface{} `json:"meta"`
			Data interface{}            `json:"data"`
		}{
			Meta: map[string]interface{}{"status": "success"},
			Data: sendMessage,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	} else {
		http.Error(w, "Client not initialized", http.StatusInternalServerError)
	}
}

func LogoutAndDeleteSession(w http.ResponseWriter, r *http.Request) {
	if client == nil {
		http.Error(w, "No active client session", http.StatusBadRequest)
		return
	}

	err := client.Logout()
	if err != nil {
		response := map[string]interface{}{
			"meta": map[string]interface{}{"status": "error", "message": "Failed to logout: " + err.Error()},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	client.Disconnect()

	response := map[string]interface{}{
		"meta": map[string]interface{}{"status": "success", "message": "Successfully logged out and session deleted."},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func main() {
	Dbase()
	router := mux.NewRouter()
	router.HandleFunc("/scan", Scan)
	router.HandleFunc("/api/kirim", Kirim).Methods("POST")
	router.HandleFunc("/api/keluarwa", LogoutAndDeleteSession).Methods("POST")
	var address = ":7000"
	fmt.Printf("Server started at %s\n", address)
	if err := http.ListenAndServe(address, router); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
