using System.Collections.Generic;
using Microsoft.AspNetCore.Mvc;
using Acme.Shop.Models;

namespace Acme.Shop.Controllers
{
    /* class Comentado {} */
    [ApiController]
    [Route("api/[controller]")]
    public class ProductosController : ControllerBase
    {
        private readonly List<Producto> _items = new();

        public ProductosController() { }

        [HttpGet]
        public IEnumerable<Producto> Listar()
        {
            return _items;
        }

        [HttpPost("nuevo")]
        public Producto Crear(Producto p) => p;

        public int Total { get; }
    }
}
