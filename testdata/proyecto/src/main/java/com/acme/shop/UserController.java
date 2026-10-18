package com.acme.shop;

import com.acme.shop.model.User;
import com.acme.shop.service.*;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

/**
 * Controlador de usuarios.
 * class Comentado { void enJavadoc() {} }
 */
@RestController
@RequestMapping("/users")
public class UserController {

    private final UserService service;
    private static final String PREFIJO = "class EnCadena { void falso() {} }";

    public UserController(UserService service) {
        this.service = service;
    }

    @GetMapping("/{id}")
    public User find(Long id) {
        return service.buscar(id);
    }

    @PostMapping
    public User create(User nuevo) {
        // void enComentario() {}
        return service.guardar(nuevo);
    }

    enum Estado { ACTIVO, INACTIVO }

    static class Interna {
        int contador;
        void incrementar() { contador++; }
    }
}
