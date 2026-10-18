package com.acme.shop.service;

import com.acme.shop.model.User;

import org.springframework.stereotype.Service;

@Service
public class UserService {

    public User buscar(Long id) {
        return new User();
    }

    public User guardar(User usuario) {
        return usuario;
    }
}
