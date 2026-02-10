package main

import (
	"context"
	"encoding/csv"
	"fmt"
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

	_ "github.com/mattn/go-sqlite3"
	qrterminal "github.com/mdp/qrterminal/v3"
)

func eventHandler(client *whatsmeow.Client, evt interface{}) {
	switch v := evt.(type) {
	case *events.Connected:
		fmt.Println("Connected to WhatsApp")

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

func main() {
	// |------------------------------------------------------------------------------------------------------|
	// | NOTE: You must also import the appropriate DB connector, e.g. github.com/mattn/go-sqlite3 for SQLite |
	// |------------------------------------------------------------------------------------------------------|

	var JID string

	if len(os.Args) > 1 {
		JID = os.Args[1]
	}

	dbLog := waLog.Stdout("Database", "DEBUG", true)
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:examplestore.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}
	// If you want multiple sessions, remember their JIDs and use .GetDevice(jid) or .GetAllDevices() instead.
	var deviceStore *store.Device
	fmt.Println(JID)
	if JID != "" {
		deviceStore, err = container.GetDevice(ctx, types.JID{Server: "s.whatsapp.net", User: JID})
	} else {
		deviceStore, err = container.GetFirstDevice(ctx)
	}
	if err != nil {
		panic(err)
	}
	if deviceStore == nil {
		deviceStore = container.NewDevice()
	}
	clientLog := waLog.Stdout("Client", "DEBUG", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(func(evt interface{}) {
		eventHandler(client, evt)
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
