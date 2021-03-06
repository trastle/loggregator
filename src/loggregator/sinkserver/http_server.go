package sinkserver

import (
	"code.google.com/p/go.net/websocket"
	"fmt"
	"github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/loggregatorlib/appid"
	"github.com/cloudfoundry/loggregatorlib/logmessage"
	"loggregator/sinks"
	"net"
	"net/http"
	"time"
)

const (
	TAIL_PATH = "/tail/"
	DUMP_PATH = "/dump/"
)

type httpServer struct {
	messageRouter           *messageRouter
	keepAliveInterval       time.Duration
	protoBufferUnmarshaller func([]byte) (*logmessage.Message, error)
	logger                  *gosteno.Logger
}

func NewHttpServer(messageRouter *messageRouter, keepAliveInterval time.Duration, protoBufferUnmarshaller func([]byte) (*logmessage.Message, error), logger *gosteno.Logger) *httpServer {
	return &httpServer{messageRouter, keepAliveInterval, protoBufferUnmarshaller, logger}
}

func (httpServer *httpServer) Start(incomingProtobufChan <-chan []byte, apiEndpoint string) {
	go httpServer.ParseEnvelopes(incomingProtobufChan)

	httpServer.logger.Infof("HttpServer: Listening for sinks at %s", apiEndpoint)
	err := http.ListenAndServe(apiEndpoint, websocket.Handler(httpServer.websocketRouter))
	if err != nil {
		panic(err)
	}
}

func (httpServer *httpServer) ParseEnvelopes(incomingProtobufChan <-chan []byte) {
	for {
		data := <-incomingProtobufChan
		message, err := httpServer.protoBufferUnmarshaller(data)
		if err != nil {
			httpServer.logger.Errorf("Log message could not be unmarshaled. Dropping it... Error: %v. Data: %v", err, data)
			continue
		}
		httpServer.messageRouter.parsedMessageChan <- message
	}
}

func (httpServer *httpServer) logInvalidApp(address net.Addr) {
	message := fmt.Sprintf("HttpServer: Did not accept sink connection with invalid app id: %s.", address)
	httpServer.logger.Warn(message)
}

func (httpServer *httpServer) websocketRouter(ws *websocket.Conn) {
	if ws.Request().URL.Path == TAIL_PATH {
		httpServer.websocketSinkHandler(ws)
	} else if ws.Request().URL.Path == DUMP_PATH {
		httpServer.dumpSinkHandler(ws)
	} else {
		ws.CloseWithStatus(400)
		return
	}
}

func (httpServer *httpServer) websocketSinkHandler(ws *websocket.Conn) {
	clientAddress := ws.RemoteAddr()
	appId := appid.FromUrl(ws.Request().URL)

	if appId == "" {
		httpServer.logInvalidApp(clientAddress)
		ws.CloseWithStatus(4000)
		return
	}

	s := sinks.NewWebsocketSink(appId, httpServer.logger, ws, clientAddress, httpServer.messageRouter.sinkCloseChan, httpServer.keepAliveInterval)
	httpServer.logger.Debugf("HttpServer: Requesting a wss sink for app %s", s.AppId())
	httpServer.messageRouter.sinkOpenChan <- s

	s.Run()
}

func (httpServer *httpServer) dumpSinkHandler(ws *websocket.Conn) {
	clientAddress := ws.RemoteAddr()
	appId := appid.FromUrl(ws.Request().URL)

	if appId == "" {
		httpServer.logInvalidApp(clientAddress)
		ws.CloseWithStatus(4000)
		return
	}

	dumpChan := httpServer.messageRouter.registerDumpChan(appId)

	dumpMessagesFromChannelToWebsocket(dumpChan, ws, clientAddress, httpServer.logger)

	ws.Close()
}

func dumpMessagesFromChannelToWebsocket(dumpChan <-chan *logmessage.Message, ws *websocket.Conn, clientAddress net.Addr, logger *gosteno.Logger) {
	for message := range dumpChan {
		err := websocket.Message.Send(ws, message.GetRawMessage())
		if err != nil {
			logger.Debugf("Dump Sink %s: Error when trying to send data to sink %s. Requesting close. Err: %v", clientAddress, err)
		} else {
			logger.Debugf("Dump Sink %s: Successfully sent data", clientAddress)
		}
	}
}

func contains(valueToFind string, values []string) bool {
	for _, value := range values {
		if valueToFind == value {
			return true
		}
	}
	return false
}
