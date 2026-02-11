package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"html/template"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"net/http"
	"net/url"
	"sync"

	"github.com/coder/websocket"

	_ "github.com/mattn/go-sqlite3"
	qrterminal "github.com/mdp/qrterminal/v3"
)

var (
	qrCode    = ""
	wsConns   = make(map[*websocket.Conn]struct{})
	wsConnsMu sync.Mutex
)

func broadcastQR(code string) {
	wsConnsMu.Lock()
	defer wsConnsMu.Unlock()
	for c := range wsConns {
		// Context for write?
		err := c.Write(context.Background(), websocket.MessageText, []byte(code))
		if err != nil {
			fmt.Println("Error broadcasting to WS:", err)
			c.Close(websocket.StatusGoingAway, "write error")
			delete(wsConns, c)
		}
	}
}

func qrHandler(w http.ResponseWriter, r *http.Request) {
	type qrData struct {
		Code string
	}
	//url encode qrCode
	qr := qrData{
		Code: qrCode,
	}
	if !strings.HasPrefix(qrCode, "http") {
		qr.Code = url.QueryEscape(qrCode)
		qr.Code = "https://api.qrserver.com/v1/create-qr-code/?size=500x500&data=" + qr.Code
	}
	fmt.Println("QR Code:", qr)
	tmpl := template.Must(template.ParseFiles("qr.html"))
	tmpl.Execute(w, qr)
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		fmt.Println("Error accepting websocket:", err)
		return
	}
	defer c.Close(websocket.StatusInternalError, "internal error")

	wsConnsMu.Lock()
	wsConns[c] = struct{}{}
	wsConnsMu.Unlock()

	// Wait specifically for close
	ctx := r.Context()
	<-ctx.Done()

	wsConnsMu.Lock()
	delete(wsConns, c)
	wsConnsMu.Unlock()
}

func eventHandler(client *whatsmeow.Client, evt interface{}, JID string) {
	switch v := evt.(type) {
	case *events.Disconnected:
		fmt.Println("Disconnected!")
		os.Exit(9)
	case *events.LoggedOut:
		fmt.Println("logout!")
		os.Exit(9)
	case *events.Connected:
		RJID := client.Store.GetJID()
		fmt.Println("RJID:", RJID)
		fmt.Println("JID:", JID)
		if RJID.User != JID {
			fmt.Println("JID Mismatch:", RJID, JID)
			qrCode = "https://cdn-icons-png.flaticon.com/128/1113/1113253.png"
			broadcastQR(qrCode)
			fmt.Println("Disconnecting...")
			err := client.Logout(context.Background())
			if err != nil {
				fmt.Println("Error logging out:", err)
			}
			os.Exit(9)
		} else {
			qrCode = "https://cdn-icons-png.flaticon.com/128/190/190411.png"
			broadcastQR(qrCode)
			fmt.Println("JID Match:", RJID, JID)
			fmt.Println("Connected!")
		}
	case *events.Message:
		message := v.Message.GetConversation()
		fmt.Println("Received a message!", message)
		//is start with !?
		if strings.HasPrefix(message, "!") {
			commandHandler(client, v)
		}
	}
}

func printQR(code string) {
	qrterminal.GenerateHalfBlock(code, qrterminal.L, os.Stdout)
}

func sendTextMessage(client *whatsmeow.Client, phone string, message string) (error, whatsmeow.SendResponse) {

	phoneJID := types.JID{Server: "s.whatsapp.net", User: phone}
	resp, err := client.SendMessage(context.Background(), phoneJID, &waE2E.Message{Conversation: proto.String(message)})
	if err != nil {
		fmt.Println("Error sending message:", err)
	}
	return err, resp
}
func sendPDFMessage(client *whatsmeow.Client, phone string, message string, pdfPath string, thumbnailPath string) (error, whatsmeow.SendResponse) {

	//exists pdf
	fileData, err := os.ReadFile(pdfPath)
	if err != nil {
		fmt.Println("Error reading file:", err)
		return err, whatsmeow.SendResponse{}
	}
	upload, err := client.Upload(context.Background(), fileData, whatsmeow.MediaDocument)
	if err != nil {
		fmt.Println("Error uploading file:", err)
		return err, whatsmeow.SendResponse{}
	}
	thumbnailData, _ := os.ReadFile(thumbnailPath)
	thumbWidth := uint32(640)
	thumbHeight := uint32(480)

	phoneJID := types.JID{Server: "s.whatsapp.net", User: phone}
	resp, err := client.SendMessage(context.Background(), phoneJID, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:             &upload.URL,
			Mimetype:        proto.String("application/pdf"), // Specify the correct MIME type
			FileSHA256:      upload.FileSHA256,
			FileLength:      &upload.FileLength,
			MediaKey:        upload.MediaKey,
			FileName:        proto.String("invitacion.pdf"), // The name to display in the chat
			FileEncSHA256:   upload.FileEncSHA256,
			DirectPath:      &upload.DirectPath,
			Caption:         proto.String(message),
			JPEGThumbnail:   thumbnailData,
			ThumbnailWidth:  &thumbWidth,
			ThumbnailHeight: &thumbHeight,
		},
	})
	if err != nil {
		fmt.Println("Error sending message:", err)
	}
	return err, resp
}
func test(client *whatsmeow.Client, tester string) (error, whatsmeow.SendResponse) {
	// read csv file ./masivo/invitados.csv
	file, err := os.Open("./masivo/invitados.csv")
	if err != nil {
		fmt.Println("Error opening file:", err)
		return err, whatsmeow.SendResponse{}
	}
	csvReader := csv.NewReader(file)
	csvReader.FieldsPerRecord = 5
	csvReader.LazyQuotes = true
	csvReader.Comma = ';'
	data, err := csvReader.ReadAll()
	if err != nil {
		fmt.Println("Error reading CSV:", err)
		return err, whatsmeow.SendResponse{}
	}
	thumbnailPath := "./masivo/thumbnail.jpeg"
	row := data[1]
	fmt.Println(row)
	pdfPath := "./masivo/invitaciones/invitaciones-" + row[0] + ".pdf"
	PhoneName := row[4]
	//phone := row[3]
	template := "Hola %s, te invitamos a nuestra boda, por favor confirma tu asistencia antes del 15 de marzo, puedes hacerlo aqui https://boda.ransato.com/"
	message := fmt.Sprintf(template, PhoneName)
	sendPDFMessage(client, tester, message, pdfPath, thumbnailPath)

	// for _, row := range data {
	// 	fmt.Println(row)
	// }
	defer file.Close()

	return nil, whatsmeow.SendResponse{}
}

func commandHandler(client *whatsmeow.Client, evt *events.Message) {

	switch message := evt.Message.GetConversation(); message {
	case "!ping":
		_, resp := sendTextMessage(client, evt.Info.Chat.User, "!pong")
		fmt.Println("!pong sent:", resp)
	case "!test":
		err, resp := test(client, evt.Info.Chat.User)
		if err != nil {
			fmt.Println("Error sending message:", err)
		}
		fmt.Println("!test sent:", resp)
	}

}

func getDeviceJIDbyPhone(phone string, sqlname string) (types.JID, error) {
	db, err := sql.Open("sqlite3", "file:"+sqlname+".db?_foreign_keys=on")
	if err != nil {
		return types.JID{}, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT jid FROM whatsmeow_device WHERE jid LIKE ?", "%"+phone+"%")
	if err != nil {
		return types.JID{}, err
	}
	defer rows.Close()
	var jid string
	for rows.Next() {
		rows.Scan(&jid)
	}
	//split @
	jid = strings.Split(jid, "@")[0]
	return types.JID{Server: "s.whatsapp.net", User: jid}, nil

}

func main() {
	// |------------------------------------------------------------------------------------------------------|
	// | NOTE: You must also import the appropriate DB connector, e.g. github.com/mattn/go-sqlite3 for SQLite |
	// |------------------------------------------------------------------------------------------------------|

	var JID string

	if len(os.Args) > 1 {
		JID = os.Args[1]
	}
	if JID == "" {
		fmt.Println("No JID provided")
		return
	}
	var port string
	var db string
	// -p --port
	flag.StringVar(&port, "p", "8080", "Port to listen on")
	flag.StringVar(&port, "port", "8080", "Port to listen on")
	// -db --database-name
	flag.StringVar(&db, "db", "sessions", "Database name")
	flag.StringVar(&db, "database-name", "sessions", "Database name")
	flag.Parse()

	dbLog := waLog.Stdout("Database", "DEBUG", true)
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+db+".db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}
	// If you want multiple sessions, remember their JIDs and use .GetDevice(jid) or .GetAllDevices() instead.
	var deviceStore *store.Device
	jjid, err := getDeviceJIDbyPhone(JID, db)
	if err != nil {
		fmt.Println("Error getting device JID:", err)
		panic(err)
	}
	fmt.Println("CURRENT-JID:", jjid)
	deviceStore, err = container.GetDevice(ctx, jjid)
	if err != nil {
		panic(err)
	}
	if deviceStore == nil {
		deviceStore = container.NewDevice()
	}

	// Start HTTP Server
	go func() {
		// Determine JID for path (if user provided one, use it, otherwise use "default" or wait until connected?
		// Actually user wanted /{jid}. If logging in, JID might not be fully known if we don't have one?
		// But if we are in QR phase, we don't have a JID yet usually?
		// "The use want ... path url is /+JID".
		// Maybe they mean the input phone number or just an identifier?
		// If os.Args[1] is provided, use that.
		pathJID := "login"
		if JID != "" {
			pathJID = JID
		} else {
			// If no JID provided, we might be registering a new one.
			// Let's use a default or wildcard?
			// The user requirement was explicit: "/"+JID.
			// If JID is empty (new login without ID arg), let's use "new".
			pathJID = "new"
		}

		fmt.Printf("Starting QR Web Server at http://localhost:8080/%s\n", pathJID)

		mux := http.NewServeMux()
		mux.HandleFunc(fmt.Sprintf("/%s", pathJID), qrHandler)
		mux.HandleFunc(fmt.Sprintf("/%s/ws", pathJID), wsHandler)

		port := "8080"
		if err := http.ListenAndServe(":"+port, mux); err != nil {
			fmt.Println("Error starting HTTP server:", err)
		}
	}()

	clientLog := waLog.Stdout("Client", "DEBUG", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(func(evt interface{}) {
		eventHandler(client, evt, JID)
	})

	if client.Store.ID == nil {
		// No ID stored, new login
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			panic(err)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				// Render the QR code here
				// e.g. qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				// or just manually `echo 2@... | qrencode -t ansiutf8` in a terminal
				fmt.Println("QR code:", evt.Code)
				printQR(evt.Code)
				qrCode = evt.Code
				go broadcastQR(evt.Code)
			} else {
				fmt.Println("Login event:", evt.Event)
			}
		}
	} else {
		// Already logged in, just connect
		err = client.Connect()
		if err != nil {
			panic(err)
		}
	}

	// Listen to Ctrl+C (you can also do something else that prevents the program from exiting)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	client.Disconnect()
}
