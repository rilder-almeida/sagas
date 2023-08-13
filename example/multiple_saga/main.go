package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/rilder-almeida/sagas"
)

type Produto struct {
	Nome  string
	Valor int
}

type Client struct {
	Nome  string
	Saldo int
}

type Estoque struct {
	Produto    Produto
	Disponivel int
}

type Compra struct {
	Cliente    *Client
	Quantidade int
	Estoque    *Estoque
}

// To run this example, execute the following command:
// go run example/multiple_saga/main.go

func main() {
	bola := Produto{
		Nome:  "Bola",
		Valor: 10,
	}

	bolaEstoque := Estoque{
		Produto:    bola,
		Disponivel: 10,
	}

	caderno := Produto{
		Nome:  "Caderno",
		Valor: 20,
	}

	cadernoEstoque := Estoque{
		Produto:    caderno,
		Disponivel: 2,
	}

	rilder := Client{
		Nome:  "Rilder",
		Saldo: 50,
	}

	joao := Client{
		Nome:  "Joao",
		Saldo: 50,
	}

	maria := Client{
		Nome:  "Maria",
		Saldo: 50,
	}

	rilderCompra := Compra{
		Cliente:    &rilder,
		Quantidade: 5,
		Estoque:    &bolaEstoque,
	}

	joaoCompra := Compra{
		Cliente:    &joao,
		Quantidade: 1,
		Estoque:    &cadernoEstoque,
	}

	mariaCompra := Compra{
		Cliente:    &maria,
		Quantidade: 2,
		Estoque:    &cadernoEstoque,
	}

	rilderCompra2 := Compra{
		Cliente:    &rilder,
		Quantidade: 2,
		Estoque:    &bolaEstoque,
	}

	sagaList := []Compra{joaoCompra, rilderCompra, mariaCompra, rilderCompra2}
	wg := sync.WaitGroup{}
	wg.Add(len(sagaList))

	for _, saga := range sagaList {
		go func(saga Compra) {
			ctxTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s, fn := makeSagaCompra(saga)
			s.Run(ctxTimeout, fn)
			wg.Done()
		}(saga)
	}

	wg.Wait()

	fmt.Println("Rilder saldo:", rilder.Saldo)
	fmt.Println("Joao saldo:", joao.Saldo)
	fmt.Println("Maria saldo:", maria.Saldo)

	fmt.Println("Bola estoque:", bolaEstoque.Disponivel)
	fmt.Println("Caderno estoque:", cadernoEstoque.Disponivel)

}

func makeSagaCompra(compra Compra) (sagas.Saga, func() bool) {

	stepSepararProduto := makeStepSepararProduto("separar_produto", &compra)
	stepVerificarSaldo := makeStepVerificarSaldo("verificar_saldo", &compra)
	stepRetornarProduto := makeStepRetornarProduto("retornar_produto", &compra)
	stepRealizarCompra := makeStepRealizarCompra("realizar_compra", &compra)
	stepReverterCompra := makeStepReverterCompra("reverter_compra", &compra)
	stepValidarCompra := makeStepValidarCompra("validar_compra", &compra)
	stepFinalizarCompra := makeStepFinalizarCompra("finalizar_compra", &compra)

	saga := sagas.NewSaga()

	saga.AddSteps(
		stepSepararProduto,
		stepRetornarProduto,
		stepVerificarSaldo,
		stepRealizarCompra,
		stepReverterCompra,
		stepValidarCompra,
		stepFinalizarCompra,
	)

	saga.When(stepSepararProduto).Is(sagas.Failed).Then(sagas.NewAction(stepFinalizarCompra.Run)).Plan()
	saga.When(stepSepararProduto).Is(sagas.Successed).Then(sagas.NewAction(stepVerificarSaldo.Run)).Plan()

	saga.When(stepVerificarSaldo).Is(sagas.Failed).Then(sagas.NewAction(stepRetornarProduto.Run)).Plan()
	saga.When(stepVerificarSaldo).Is(sagas.Successed).Then(sagas.NewAction(stepRealizarCompra.Run)).Plan()

	saga.When(stepRealizarCompra).Is(sagas.Failed).Then(sagas.NewAction(stepRetornarProduto.Run)).Plan()
	saga.When(stepRealizarCompra).Is(sagas.Successed).Then(sagas.NewAction(stepValidarCompra.Run)).Plan()

	saga.When(stepValidarCompra).Is(sagas.Failed).Then(sagas.NewAction(stepReverterCompra.Run)).Plan()
	saga.When(stepValidarCompra).Is(sagas.Successed).Then(sagas.NewAction(stepFinalizarCompra.Run)).Plan()

	saga.When(stepReverterCompra).Is(sagas.Completed).Then(sagas.NewAction(stepRetornarProduto.Run)).Plan()
	saga.When(stepRetornarProduto).Is(sagas.Completed).Then(sagas.NewAction(stepFinalizarCompra.Run)).Plan()

	return saga, func() bool {
		return stepFinalizarCompra.GetState() == sagas.Completed
	}
}

func makeStepSepararProduto(nomeStep string, compra *Compra) sagas.Step {
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second))

	actionFn := func(ctx context.Context) error {

		if compra.Estoque.Disponivel < compra.Quantidade {
			log.Println("estoque insuficiente: ", compra.Estoque.Produto.Nome, compra.Estoque.Disponivel)
			return errors.New("estoque insuficiente")
		}
		log.Println("separando produto: ", compra.Estoque.Produto.Nome, compra.Quantidade)
		compra.Estoque.Disponivel -= compra.Quantidade

		return nil
	}

	return sagas.NewStep(
		nomeStep,
		actionFn,
		sagas.WithStepRetrier(retrier),
	)
}

func makeStepRetornarProduto(nomeStep string, compra *Compra) sagas.Step {
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second))

	action := func(ctx context.Context) error {
		log.Println("retornando produto: ", compra.Estoque.Produto.Nome, compra.Quantidade)
		compra.Estoque.Disponivel += compra.Quantidade
		return nil
	}

	return sagas.NewStep(
		nomeStep,
		action,
		sagas.WithStepRetrier(retrier),
	)
}

func makeStepVerificarSaldo(nomeStep string, compra *Compra) sagas.Step {
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second))

	action := func(ctx context.Context) error {

		if compra.Cliente.Saldo < (compra.Estoque.Produto.Valor * compra.Quantidade) {
			log.Println("saldo insuficiente: ", compra.Cliente.Nome, compra.Cliente.Saldo)
			return errors.New("stepVerificarSaldo: saldo insuficiente " + compra.Cliente.Nome + " " + fmt.Sprint(compra.Cliente.Saldo))
		}
		log.Println("verificando saldo: ", compra.Cliente.Nome, compra.Cliente.Saldo)
		return nil
	}

	return sagas.NewStep(
		nomeStep,
		action,
		sagas.WithStepRetrier(retrier),
	)
}

func makeStepRealizarCompra(nomeStep string, compra *Compra) sagas.Step {
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second))

	action := func(ctx context.Context) error {
		log.Println("realizando compra: ", compra.Cliente.Nome, compra.Estoque.Produto.Nome, compra.Quantidade)
		compra.Cliente.Saldo -= (compra.Estoque.Produto.Valor * compra.Quantidade)
		return nil
	}

	return sagas.NewStep(
		nomeStep,
		action,
		sagas.WithStepRetrier(retrier),
	)
}

func makeStepReverterCompra(nomeStep string, compra *Compra) sagas.Step {
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second))

	action := func(ctx context.Context) error {
		log.Println("revertendo compra: ", compra.Cliente.Nome, compra.Estoque.Produto.Nome, compra.Quantidade)
		compra.Cliente.Saldo += (compra.Estoque.Produto.Valor * compra.Quantidade)
		return nil
	}

	return sagas.NewStep(
		nomeStep,
		action,
		sagas.WithStepRetrier(retrier),
	)
}

func makeStepValidarCompra(nomeStep string, compra *Compra) sagas.Step {
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second))

	action := func(ctx context.Context) error {

		if compra.Cliente.Saldo < 0 {
			log.Println("saldo negativo: ", compra.Cliente.Nome, compra.Cliente.Saldo)
			return errors.New("stepValidarCompra: saldo negativo " + compra.Cliente.Nome + " " + fmt.Sprint(compra.Cliente.Saldo))
		}

		if compra.Estoque.Disponivel < 0 {
			log.Println("estoque negativo: ", compra.Estoque.Produto.Nome, compra.Estoque.Disponivel)
			return errors.New("stepValidarCompra: estoque negativo " + compra.Estoque.Produto.Nome + " " + fmt.Sprint(compra.Estoque.Disponivel))
		}

		log.Println("validando compra: ", compra.Cliente.Nome, compra.Estoque.Produto.Nome, compra.Quantidade)
		return nil
	}

	return sagas.NewStep(
		nomeStep,
		action,
		sagas.WithStepRetrier(retrier),
	)
}

func makeStepFinalizarCompra(nomeStep string, compra *Compra) sagas.Step {
	retrier := sagas.NewRetrier(sagas.BackoffConstant(3, 1*time.Second))

	action := func(ctx context.Context) error {
		log.Println("finalizando compra: ", compra.Cliente.Nome, compra.Estoque.Produto.Nome, compra.Quantidade)
		return nil
	}

	return sagas.NewStep(
		nomeStep,
		action,
		sagas.WithStepRetrier(retrier),
	)
}
