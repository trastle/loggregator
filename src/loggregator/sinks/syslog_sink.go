package sinks

import (
	"fmt"
	"github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/loggregatorlib/cfcomponent/instrumentation"
	"github.com/cloudfoundry/loggregatorlib/logmessage"
	"math"
	"math/rand"
	"sync/atomic"
	"time"
)

type SyslogSink struct {
	logger           *gosteno.Logger
	appId            string
	drainUrl         string
	sentMessageCount *uint64
	sentByteCount    *uint64
	listenerChannel  chan *logmessage.Message
	syslogWriter     SyslogWriter
	errorChannel     chan<- *logmessage.Message
}

func NewSyslogSink(appId string, drainUrl string, givenLogger *gosteno.Logger, syslogWriter SyslogWriter, errorChannel chan<- *logmessage.Message) Sink {
	givenLogger.Debugf("Syslog Sink %s: Created for appId [%s]", drainUrl, appId)
	return &SyslogSink{
		appId:            appId,
		drainUrl:         drainUrl,
		logger:           givenLogger,
		sentMessageCount: new(uint64),
		sentByteCount:    new(uint64),
		listenerChannel:  make(chan *logmessage.Message),
		syslogWriter:     syslogWriter,
		errorChannel:     errorChannel,
	}
}

func (s *SyslogSink) Run() {
	s.logger.Infof("Syslog Sink %s: Running.", s.drainUrl)
	defer s.logger.Errorf("Syslog Sink %s: Stopped. This should never happen", s.drainUrl)

	backoffStrategy := newExponentialRetryStrategy()
	numberOfTries := 0

	buffer := runTruncatingBuffer(s, 100, s.Logger())
	for {
		s.logger.Debugf("Syslog Sink %s: Starting loop. Current backoff: %v", s.drainUrl, backoffStrategy(numberOfTries))

		time.Sleep(backoffStrategy(numberOfTries))
		if !s.syslogWriter.IsConnected() {
			s.logger.Debugf("Syslog Sink %s: Not connected. Trying to connect.", s.drainUrl)
			err := s.syslogWriter.Connect()
			if err != nil {
				errorMsg := fmt.Sprintf("Syslog Sink %s: Error when dialing out. Backing off for %v. Err: %v", s.drainUrl, backoffStrategy(numberOfTries+1), err)
				numberOfTries++

				s.logger.Warnf(errorMsg)
				logMessage, err := logmessage.GenerateMessage(logmessage.LogMessage_ERR, logmessage.LogMessage_LOGGREGATOR, errorMsg, s.appId, "LGR")
				if err == nil {
					s.errorChannel <- logMessage
				} else {
					s.logger.Warnf("Error marshalling message: %v", err)
				}
				continue
			}
			s.logger.Infof("Syslog Sink %s: successfully connected.", s.drainUrl)
			s.syslogWriter.SetConnected(true)
			numberOfTries = 0
			defer s.syslogWriter.Close()
		}

		s.logger.Debugf("Syslog Sink %s: Waiting for activity\n", s.drainUrl)
		message, ok := <-buffer.GetOutputChannel()
		if !ok {
			s.logger.Debugf("Syslog Sink %s: Closed listener channel detected. Closing.\n", s.drainUrl)
			return
		}
		s.logger.Debugf("Syslog Sink %s: Got %d bytes. Sending data\n", s.drainUrl, message.GetRawMessageLength())

		var err error

		switch message.GetLogMessage().GetMessageType() {
		case logmessage.LogMessage_OUT:
			_, err = s.syslogWriter.WriteStdout(message.GetLogMessage().GetMessage(), message.GetShortSourceTypeName(), *message.GetLogMessage().Timestamp)
		case logmessage.LogMessage_ERR:
			_, err = s.syslogWriter.WriteStderr(message.GetLogMessage().GetMessage(), message.GetShortSourceTypeName(), *message.GetLogMessage().Timestamp)
		}
		if err != nil {
			s.logger.Debugf("Syslog Sink %s: Error when trying to send data to sink. Backing off. Err: %v\n", s.drainUrl, err)
			numberOfTries++
			s.syslogWriter.SetConnected(false)
		} else {
			s.logger.Debugf("Syslog Sink %s: Successfully sent data\n", s.drainUrl)
			numberOfTries = 0
			atomic.AddUint64(s.sentMessageCount, 1)
			atomic.AddUint64(s.sentByteCount, uint64(message.GetRawMessageLength()))
		}
	}
}

func (s *SyslogSink) Channel() chan *logmessage.Message {
	return s.listenerChannel
}

func (s *SyslogSink) Logger() *gosteno.Logger {
	return s.logger
}

func (s *SyslogSink) Identifier() string {
	return s.drainUrl
}

func (s *SyslogSink) AppId() string {
	return s.appId
}

func (s *SyslogSink) ShouldReceiveErrors() bool {
	return false
}

func (s *SyslogSink) Emit() instrumentation.Context {
	return instrumentation.Context{Name: "syslogSink",
		Metrics: []instrumentation.Metric{
			instrumentation.Metric{Name: "sentMessageCount:" + s.appId, Value: atomic.LoadUint64(s.sentMessageCount)},
			instrumentation.Metric{Name: "sentByteCount:" + s.appId, Value: atomic.LoadUint64(s.sentByteCount)},
		},
	}
}

type retryStrategy func(counter int) time.Duration

func newExponentialRetryStrategy() retryStrategy {
	exponential := func(counter int) time.Duration {
		if counter == 0 {
			return time.Duration(0)
		}
		if counter > 23 {
			counter = 23
		}
		tenthDuration := int(math.Pow(2, float64(counter-1)) * 100)
		duration := tenthDuration * 10
		randomOffset := rand.Intn(tenthDuration*2) - tenthDuration
		return (time.Duration(duration) * time.Microsecond) + (time.Duration(randomOffset) * time.Microsecond)
	}
	return exponential
}
