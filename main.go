package sagas

// import (
// 	"context"
// 	"errors"
// 	"fmt"
// 	"sync"
// 	"time"
// )

// type Produto struct {
// 	Nome  string
// 	Valor int
// }

// type Client struct {
// 	Nome  string
// 	Saldo int
// }

// type Estoque struct {
// 	Produto    Produto
// 	Disponivel int
// }

// type Compra struct {
// 	Cliente    *Client
// 	Quantidade int
// 	Estoque    *Estoque
// }

// func Test_Example() {
// 	bola := Produto{
// 		Nome:  "Bola",
// 		Valor: 10,
// 	}

// 	bolaEstoque := Estoque{
// 		Produto:    bola,
// 		Disponivel: 10,
// 	}

// 	caderno := Produto{
// 		Nome:  "Caderno",
// 		Valor: 20,
// 	}

// 	cadernoEstoque := Estoque{
// 		Produto:    caderno,
// 		Disponivel: 2,
// 	}

// 	rilder := Client{
// 		Nome:  "Rilder",
// 		Saldo: 50,
// 	}

// 	joao := Client{
// 		Nome:  "Joao",
// 		Saldo: 50,
// 	}

// 	maria := Client{
// 		Nome:  "Maria",
// 		Saldo: 50,
// 	}

// 	rilderCompra := Compra{
// 		Cliente:    &rilder,
// 		Quantidade: 5,
// 		Estoque:    &bolaEstoque,
// 	}

// 	joaoCompra := Compra{
// 		Cliente:    &joao,
// 		Quantidade: 1,
// 		Estoque:    &cadernoEstoque,
// 	}

// 	mariaCompra := Compra{
// 		Cliente:    &maria,
// 		Quantidade: 2,
// 		Estoque:    &cadernoEstoque,
// 	}

// 	rilderCompra2 := Compra{
// 		Cliente:    &rilder,
// 		Quantidade: 2,
// 		Estoque:    &bolaEstoque,
// 	}

// 	sagaList := []Compra{joaoCompra, rilderCompra, mariaCompra, rilderCompra2}
// 	wg := sync.WaitGroup{}
// 	wg.Add(len(sagaList))

// 	for _, saga := range sagaList {
// 		go func(saga Compra) {
// 			ctxTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
// 			defer cancel()
// 			controler, fn := makeSagaCompra(saga)
// 			controler.Run(ctxTimeout, fn)
// 			wg.Done()
// 		}(saga)
// 	}

// 	wg.Wait()

// 	fmt.Println("Bola: ", bolaEstoque.Disponivel)
// 	fmt.Println("Caderno: ", cadernoEstoque.Disponivel)

// 	fmt.Println("Rilder: ", rilder.Saldo)
// 	fmt.Println("Joao: ", joao.Saldo)
// 	fmt.Println("Maria: ", maria.Saldo)

// }

// func makeSagaCompra(compra Compra) (*Controller, func() bool) {

// 	stepSepararProduto := makeStepSepararProduto("separar_produto", &compra)
// 	stepVerificarSaldo := makeStepVerificarSaldo("verificar_saldo", &compra)
// 	stepRetornarProduto := makeStepRetornarProduto("retornar_produto", &compra)
// 	stepRealizarCompra := makeStepRealizarCompra("realizar_compra", &compra)
// 	stepReverterCompra := makeStepReverterCompra("reverter_compra", &compra)
// 	stepValidarCompra := makeStepValidarCompra("validar_compra", &compra)
// 	stepFinalizarCompra := makeStepFinalizarCompra("finalizar_compra", &compra)

// 	controller := NewController()

// 	controller.AddSteps(
// 		stepSepararProduto,
// 		stepRetornarProduto,
// 		stepVerificarSaldo,
// 		stepRealizarCompra,
// 		stepReverterCompra,
// 		stepValidarCompra,
// 		stepFinalizarCompra,
// 	)

// 	controller.When(stepSepararProduto).Is(Failed).Then(stepFinalizarCompra.Run).Plan()
// 	controller.When(stepSepararProduto).Is(Successed).Then(stepVerificarSaldo.Run).Plan()

// 	controller.When(stepVerificarSaldo).Is(Failed).Then(stepRetornarProduto.Run).Plan()
// 	controller.When(stepVerificarSaldo).Is(Successed).Then(stepRealizarCompra.Run).Plan()

// 	controller.When(stepRealizarCompra).Is(Failed).Then(stepRetornarProduto.Run).Plan()
// 	controller.When(stepRealizarCompra).Is(Successed).Then(stepValidarCompra.Run).Plan()

// 	controller.When(stepValidarCompra).Is(Failed).Then(stepReverterCompra.Run).Plan()
// 	controller.When(stepValidarCompra).Is(Successed).Then(stepFinalizarCompra.Run).Plan()

// 	controller.When(stepReverterCompra).Is(Completed).Then(stepRetornarProduto.Run).Plan()
// 	controller.When(stepRetornarProduto).Is(Completed).Then(stepFinalizarCompra.Run).Plan()

// 	return controller, func() bool {
// 		return stepFinalizarCompra.GetState() == Completed
// 	}
// }

// func makeStepSepararProduto(nomeStep string, compra *Compra) *Step {
// 	retrier := NewRetrier(ConstantBackoff(3, 1*time.Second), nil)

// 	actionFn := func(ctx context.Context) error {
// 		if compra.Estoque.Disponivel < compra.Quantidade {
// 			return errors.New("estoque insuficiente")
// 		}
// 		compra.Estoque.Disponivel -= compra.Quantidade
// 		return nil
// 	}

// 	return NewStep(nomeStep, actionFn, retrier)
// }

// func makeStepRetornarProduto(nomeStep string, compra *Compra) *Step {
// 	retrier := NewRetrier(ConstantBackoff(3, 1*time.Second), nil)

// 	action := func(ctx context.Context) error {
// 		compra.Estoque.Disponivel += compra.Quantidade
// 		return nil
// 	}

// 	return NewStep(nomeStep, action, retrier)
// }

// func makeStepVerificarSaldo(nomeStep string, compra *Compra) *Step {
// 	retrier := NewRetrier(ConstantBackoff(3, 1*time.Second), nil)

// 	action := func(ctx context.Context) error {
// 		if compra.Cliente.Saldo < (compra.Estoque.Produto.Valor * compra.Quantidade) {
// 			return errors.New("saldo insuficiente")
// 		}
// 		return nil
// 	}

// 	return NewStep(nomeStep, action, retrier)
// }

// func makeStepRealizarCompra(nomeStep string, compra *Compra) *Step {
// 	retrier := NewRetrier(ConstantBackoff(3, 1*time.Second), nil)

// 	action := func(ctx context.Context) error {
// 		compra.Cliente.Saldo -= (compra.Estoque.Produto.Valor * compra.Quantidade)
// 		return nil
// 	}

// 	return NewStep(nomeStep, action, retrier)
// }

// func makeStepReverterCompra(nomeStep string, compra *Compra) *Step {
// 	retrier := NewRetrier(ConstantBackoff(3, 1*time.Second), nil)

// 	action := func(ctx context.Context) error {
// 		compra.Cliente.Saldo += (compra.Estoque.Produto.Valor * compra.Quantidade)
// 		return nil
// 	}

// 	return NewStep(nomeStep, action, retrier)
// }

// func makeStepValidarCompra(nomeStep string, compra *Compra) *Step {
// 	retrier := NewRetrier(ConstantBackoff(3, 1*time.Second), nil)

// 	action := func(ctx context.Context) error {
// 		if compra.Cliente.Saldo < 0 {
// 			return errors.New("saldo insuficiente")
// 		}

// 		if compra.Estoque.Disponivel < 0 {
// 			return errors.New("estoque insuficiente")
// 		}
// 		return nil
// 	}

// 	return NewStep(nomeStep, action, retrier)
// }

// func makeStepFinalizarCompra(nomeStep string, compra *Compra) *Step {
// 	retrier := NewRetrier(ConstantBackoff(3, 1*time.Second), nil)

// 	action := func(ctx context.Context) error {
// 		return nil
// 	}

// 	return NewStep(nomeStep, action, retrier)
// }
