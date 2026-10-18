using System;

namespace Acme.Shop.Models
{
    public record Producto(int Id, string Nombre);

    public class Categoria
    {
        public string Texto = @"class EnVerbatim { }";
        public int Codigo { get; set; }
    }
}
