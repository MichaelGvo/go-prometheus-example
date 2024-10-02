package main

import (
	"context"
	"errors"
	"flag"
	"math"
	"math/rand"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/metrics"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/ThreeDotsLabs/watermill/message/router/plugin"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
)

var (
	metricsAddr  = flag.String("metrics", ":8081", "Адрес, на котором будет доступен /metrics для Prometheus")
	handlerDelay = flag.Float64("delay", 0, "stdev нормального распределения задержки в обработчике (в секундах), чтобы имитировать нагрузку")

	logger = watermill.NewStdLogger(true, true)
	random = rand.New(rand.NewSource(time.Now().Unix()))
)

func delay() {
	seconds := *handlerDelay
	if seconds == 0 {
		return
	}
	delay := math.Abs(random.NormFloat64() * seconds)
	time.Sleep(time.Duration(float64(time.Second) * delay))
}

// handler публикует 0-4 сообщения с случайной задержкой.
func handler(msg *message.Message) ([]*message.Message, error) {
	delay()

	numOutgoing := random.Intn(4)
	outgoing := make([]*message.Message, numOutgoing)

	for i := 0; i < numOutgoing; i++ {
		outgoing[i] = msg.Copy()
	}
	return outgoing, nil
}

// consumeMessages потребляет сообщения, выходящие из handler.
func consumeMessages(subscriber message.Subscriber) {
	messages, err := subscriber.Subscribe(context.Background(), "pub_topic")
	if err != nil {
		panic(err)
	}

	for msg := range messages {
		msg.Ack()
	}
}

// produceMessages производит входящие сообщения с задержками 50-100 миллисекунд.
func produceMessages(routerClosed chan struct{}, publisher message.Publisher) {
	for {
		select {
		case <-routerClosed:
			return
		default:

		}

		time.Sleep(50*time.Millisecond + time.Duration(random.Intn(50))*time.Millisecond)
		msg := message.NewMessage(watermill.NewUUID(), []byte{})
		_ = publisher.Publish("sub_topic", msg)
	}
}

func main() {
	flag.Parse()

	pubSub := gochannel.NewGoChannel(gochannel.Config{}, logger)

	router, err := message.NewRouter(
		message.RouterConfig{},
		logger,
	)
	if err != nil {
		panic(err)
	}

	prometheusRegistry, closeMetricsServer := metrics.CreateRegistryAndServeHTTP(*metricsAddr)
	defer closeMetricsServer()

	// оставляем namespace и subsystem пустыми
	metricsBuilder := metrics.NewPrometheusMetricsBuilder(prometheusRegistry, "", "")
	metricsBuilder.AddPrometheusRouterMetrics(router)

	router.AddMiddleware(
		middleware.Recoverer,
		middleware.RandomFail(0.1),
		middleware.RandomPanic(0.1),
	)
	router.AddPlugin(plugin.SignalsHandler)

	router.AddHandler(
		"metrics-example",
		"sub_topic",
		pubSub,
		"pub_topic",
		pubSub,
		handler,
	)

	pub := randomFailPublisherDecorator{pubSub, 0.1}

	// publisher и subscriber будут озаглавлены AddPrometheusRouterMetrics.
	// Мы используем тот же pub/sub для генерации входящих сообщений в handler
	// и потребления исходящих сообщений.
	// Они будут иметь метку handler_name=<no handler> в Prometheus.
	subWithMetrics, err := metricsBuilder.DecorateSubscriber(pubSub)
	if err != nil {
		panic(err)
	}
	pubWithMetrics, err := metricsBuilder.DecoratePublisher(pub)
	if err != nil {
		panic(err)
	}

	routerClosed := make(chan struct{})
	go produceMessages(routerClosed, pubWithMetrics)
	go consumeMessages(subWithMetrics)

	_ = router.Run(context.Background())
	close(routerClosed)
}

type randomFailPublisherDecorator struct {
	message.Publisher
	failProbability float64
}

func (r randomFailPublisherDecorator) Publish(topic string, messages ...*message.Message) error {
	if random.Float64() < r.failProbability {
		return errors.New("случайный сбой при публикации")
	}
	return r.Publisher.Publish(topic, messages...)
}
