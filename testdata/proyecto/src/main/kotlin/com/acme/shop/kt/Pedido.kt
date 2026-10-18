package com.acme.shop.kt

import com.acme.shop.model.User

/* class ComentadoKt */
data class Pedido(val id: Long, val total: Double, val dueno: User? = null)

object Registro {
    val pedidos = mutableListOf<Pedido>()

    fun agregar(p: Pedido) {
        pedidos.add(p)
    }
}

interface Repositorio<T> {
    fun todos(): List<T>
}

@Service
class PedidoService(private val repo: Repositorio<Pedido>) {

    @GetMapping("/pedidos")
    fun listar(): List<Pedido> = repo.todos()

    val plantilla = """
        class EnRawString { fun falsa() {} }
    """

    companion object {
        const val MAX = 10
    }
}

enum class Estado { NUEVO, PAGADO }

fun resumen(p: Pedido): String = "Pedido ${p.id}"
