/* -----------------------------------------------
/* Original Author : Vincent Garreau  - vincentgarreau.com
/* MIT license: http://opensource.org/licenses/MIT
/* Original GitHub : github.com/VincentGarreau/particles.js
/* 
/* This has been modified by me (hecate) as follows:
/* - framerate normalization based on frametime
/* - compacted down for my specific website (rolled configs in, removed unused bits)
/* ----------------------------------------------- */

var previousFrameTime = null; //hackate :3

var pJS = function(tag_id) {
  var canvas_el = document.querySelector('#'+tag_id+' > .particles-js-canvas-el');

  /* default object with minimal structure */
  this.pJS = { 
    canvas: {
      el: canvas_el,
      w: canvas_el.offsetWidth,
      h: canvas_el.offsetHeight
    },
    particles: {
      array: []
    },
    interactivity: {
      mouse:{}
    },
    fn: {
      interact: {},
      modes: {},
      vendors:{}
    },
    tmp: {}
  };

  // actual config
  var config = {
    particles: {
      color: '#f15c99',
      number: 130,
      size: 3,
      line_linked: {
        distance: 100,
        color: "#f15c99",
        opacity: 0.4,
      },
      move: {
        speed: 5,
        random: true,
        out_mode: "out",
        attract: {
          enable: true,
          rotateX: 75,
          rotateY: 75
        }
      }
    },
    interactivity: {
      events: {
        onhover: {
          enable: true,
          mode: ["grab", "attract"]
        },
        resize: true
      },
      modes: {
        grab: {
          distance: 314,
          line_linked: {
            opacity: 1
          }
        },
      }
    },
    retina_detect: true,
  };

  Object.deepExtend(this.pJS, config);

  var pJS = this.pJS;

  pJS.tmp.obj = { // todo: i don't like this
    size_value: pJS.particles.size,
    move_speed: pJS.particles.move.speed,
    line_linked_distance: pJS.particles.line_linked.distance,
    line_linked_width: pJS.particles.line_linked.width,
    mode_grab_distance: pJS.interactivity.modes.grab.distance,
  };


  pJS.fn.retinaInit = function() {
    if(pJS.retina_detect && window.devicePixelRatio > 1){
      pJS.canvas.pxratio = window.devicePixelRatio; 
      pJS.tmp.retina = true;
    } 
    else{
      pJS.canvas.pxratio = 1;
      pJS.tmp.retina = false;
    }

    pJS.canvas.w = pJS.canvas.el.offsetWidth * pJS.canvas.pxratio;
    pJS.canvas.h = pJS.canvas.el.offsetHeight * pJS.canvas.pxratio;

    pJS.particles.size = pJS.tmp.obj.size_value * pJS.canvas.pxratio;
    pJS.particles.move.speed = pJS.tmp.obj.move_speed * pJS.canvas.pxratio;
    pJS.particles.line_linked.distance = pJS.tmp.obj.line_linked_distance * pJS.canvas.pxratio;
    pJS.interactivity.modes.grab.distance = pJS.tmp.obj.mode_grab_distance * pJS.canvas.pxratio;
    pJS.particles.line_linked.width = pJS.tmp.obj.line_linked_width * pJS.canvas.pxratio;
  };

  /* ---------- pJS functions - canvas ------------ */
  pJS.fn.canvasInit = function(){
    pJS.canvas.ctx = pJS.canvas.el.getContext('2d');
  };

  pJS.fn.canvasSize = function(){
    pJS.canvas.el.width = pJS.canvas.w;
    pJS.canvas.el.height = pJS.canvas.h;
    if(pJS && pJS.interactivity.events.resize) {
      window.addEventListener('resize', function() {
          pJS.canvas.w = pJS.canvas.el.offsetWidth;
          pJS.canvas.h = pJS.canvas.el.offsetHeight;

          /* resize canvas */
          if(pJS.tmp.retina) {
            pJS.canvas.w *= pJS.canvas.pxratio;
            pJS.canvas.h *= pJS.canvas.pxratio;
          }

          pJS.canvas.el.width = pJS.canvas.w;
          pJS.canvas.el.height = pJS.canvas.h;
      });
    }
  };

  pJS.fn.canvasPaint = function(){
    pJS.canvas.ctx.fillRect(0, 0, pJS.canvas.w, pJS.canvas.h);
  };

  pJS.fn.canvasClear = function(){
    pJS.canvas.ctx.clearRect(0, 0, pJS.canvas.w, pJS.canvas.h);
  };
  /* --------- pJS functions - particles ----------- */
  pJS.fn.particle = function(color, position) {
    this.radius = Math.random() * pJS.particles.size;
    this.x = position ? position.x : Math.random() * pJS.canvas.w;
    this.y = position ? position.y : Math.random() * pJS.canvas.h;

    /* check position  - into the canvas */
    if(this.x > pJS.canvas.w - this.radius*2) this.x = this.x - this.radius;
    else if(this.x < this.radius*2) this.x = this.x + this.radius;
    if(this.y > pJS.canvas.h - this.radius*2) this.y = this.y - this.radius;
    else if(this.y < this.radius*2) this.y = this.y + this.radius;

    this.color = color;
    this.opacity = Math.random();

    /* velocity */ 
    this.vx = Math.random() - 0.5;
    this.vy = Math.random() - 0.5;
    this.vx_i = this.vx;
    this.vy_i = this.vy;
  };

  pJS.fn.particle.prototype.draw = function() {
    var p = this;
    pJS.canvas.ctx.fillStyle = 'rgba('+p.color.r+','+p.color.g+','+p.color.b+','+p.opacity+')';

    pJS.canvas.ctx.beginPath();
    pJS.canvas.ctx.arc(p.x, p.y, p.radius, 0, Math.PI * 2, false);
    pJS.canvas.ctx.closePath();
    
    pJS.canvas.ctx.fill();
  };

  pJS.fn.particlesCreate = function(){
    var color = hexToRgb(pJS.particles.color);
    for(var i = 0; i < pJS.particles.number; i++) {
      pJS.particles.array.push(new pJS.fn.particle(color));
    }
  };

  pJS.fn.particlesUpdate = function(){
    // begin hackate fix for framerate normalization
    let currentFrameTime = performance.now();
    let move_speed_scalar = 1.0;
    if (previousFrameTime != null) {
      let deltaTime = currentFrameTime - previousFrameTime;
      const fixedFrameRate = 60;
      let actualFrameRate = 1 / deltaTime * 1000; // deltaTime is in milliseconds, so we convert it to seconds
      move_speed_scalar = fixedFrameRate / actualFrameRate;
    }
    previousFrameTime = currentFrameTime;
    // end most of the hackate fix
    for(var i = 0; i < pJS.particles.array.length; i++){
      var p = pJS.particles.array[i];

      var ms = pJS.particles.move.speed/2 * move_speed_scalar; //also hackate fix
      p.x += p.vx * ms;
      p.y += p.vy * ms;

      /* change particle position if it is out of canvas */
      var new_pos = {
        x_left: -p.radius,
        x_right: pJS.canvas.w + p.radius,
        y_top: -p.radius,
        y_bottom: pJS.canvas.h + p.radius
      }

      if(p.x - p.radius > pJS.canvas.w){
        p.x = new_pos.x_left;
        p.y = Math.random() * pJS.canvas.h;
      }
      else if(p.x + p.radius < 0){
        p.x = new_pos.x_right;
        p.y = Math.random() * pJS.canvas.h;
      }
      if(p.y - p.radius > pJS.canvas.h){
        p.y = new_pos.y_top;
        p.x = Math.random() * pJS.canvas.w;
      }
      else if(p.y + p.radius < 0){
        p.y = new_pos.y_bottom;
        p.x = Math.random() * pJS.canvas.w;
      }

      /* events */
      if(isInArray('grab', pJS.interactivity.events.onhover.mode)) {
        pJS.fn.modes.grabParticle(p);
      }

      if(isInArray('attract', pJS.interactivity.events.onhover.mode)) {
        pJS.fn.modes.attractParticle(p);
      }

      /* interaction auto between particles */
      for(var j = i + 1; j < pJS.particles.array.length; j++){
        var p2 = pJS.particles.array[j];
        /* attract particles */
        if(pJS.particles.move.attract.enable){
          pJS.fn.interact.attractParticles(p,p2);
        }
      }
    }
  };

  pJS.fn.particlesDraw = function(){
    /* clear canvas, update each particle, draw each particle */
    pJS.canvas.ctx.clearRect(0, 0, pJS.canvas.w, pJS.canvas.h);
    pJS.fn.particlesUpdate();
    for(var i = 0; i < pJS.particles.array.length; i++){
      pJS.particles.array[i].draw();
    }
  };

  pJS.fn.interact.attractParticles  = function(p1, p2){
    /* condensed particles */
    var dx = p1.x - p2.x,
        dy = p1.y - p2.y,
        dist = Math.sqrt(dx*dx + dy*dy);


    if(dist <= pJS.particles.line_linked.distance) {
      var ax = dx/(pJS.particles.move.attract.rotateX*1000),
          ay = dy/(pJS.particles.move.attract.rotateY*1000);

      p1.vx -= ax;
      p1.vy -= ay;

      p2.vx += ax;
      p2.vy += ay;
    }
  }

  pJS.fn.modes.grabParticle = function(p) {
    if(pJS.interactivity.events.onhover.enable && pJS.interactivity.status == 'mousemove'){
      var dx_mouse = p.x - pJS.interactivity.mouse.pos_x,
          dy_mouse = p.y - pJS.interactivity.mouse.pos_y,
          dist_mouse = Math.sqrt(dx_mouse*dx_mouse + dy_mouse*dy_mouse);

      /* draw a line between the cursor and the particle if the distance between them is under the config distance */
      if(dist_mouse <= pJS.interactivity.modes.grab.distance){
        var opacity_line = pJS.interactivity.modes.grab.line_linked.opacity - (dist_mouse / (1/pJS.interactivity.modes.grab.line_linked.opacity)) / pJS.interactivity.modes.grab.distance;
        /* style */
        var color_line = pJS.particles.line_linked.color_rgb_line;
        pJS.canvas.ctx.strokeStyle = 'rgba('+color_line.r+','+color_line.g+','+color_line.b+','+opacity_line+')';
        pJS.canvas.ctx.lineWidth = 1;
        
        /* path */
        pJS.canvas.ctx.beginPath();
        pJS.canvas.ctx.moveTo(p.x, p.y);
        pJS.canvas.ctx.lineTo(pJS.interactivity.mouse.pos_x, pJS.interactivity.mouse.pos_y);
        pJS.canvas.ctx.stroke();
        pJS.canvas.ctx.closePath();
      }
    }
  };

  pJS.fn.modes.attractParticle = function(p){
    pJS.fn.interact.attractParticles(p, {
        x: pJS.interactivity.mouse.pos_x,
        y: pJS.interactivity.mouse.pos_y,
        vx: 0, vy: 0
    });
  };

  /* ---------- pJS functions - vendors ------------ */
  pJS.fn.vendors.eventsListeners = function(){
    /* events target element */
    pJS.interactivity.el = window;
    pJS.interactivity.el.addEventListener('mousemove', function(e){
      pJS.interactivity.mouse.pos_x = e.clientX;
      pJS.interactivity.mouse.pos_y = e.clientY;
      if(pJS.tmp.retina){
        pJS.interactivity.mouse.pos_x *= pJS.canvas.pxratio;
        pJS.interactivity.mouse.pos_y *= pJS.canvas.pxratio;
      }
      pJS.interactivity.status = 'mousemove';
    });
  };

  pJS.fn.vendors.draw = function(){
    pJS.fn.particlesDraw();
    pJS.fn.drawAnimFrame = requestAnimFrame(pJS.fn.vendors.draw);
  };

  pJS.fn.vendors.checkBeforeDraw = function(){
    pJS.fn.vendors.init();
    pJS.fn.vendors.draw();
  };

  pJS.fn.vendors.init = function(){
    /* init canvas + particles */
    pJS.fn.retinaInit();
    pJS.fn.canvasInit();
    pJS.fn.canvasSize();
    pJS.fn.canvasPaint();
    pJS.fn.particlesCreate();
    pJS.particles.line_linked.color_rgb_line = hexToRgb(pJS.particles.line_linked.color);
  };

  pJS.fn.vendors.start = function(){
    pJS.fn.vendors.checkBeforeDraw();
  };

  /* ---------- pJS - start ------------ */
  pJS.fn.vendors.eventsListeners();
  pJS.fn.vendors.start();
};

/* ---------- global functions - vendors ------------ */
Object.deepExtend = function(destination, source) {
  for (var property in source) {
    if (source[property] && source[property].constructor &&
     source[property].constructor === Object) {
      destination[property] = destination[property] || {};
      arguments.callee(destination[property], source[property]);
    } else {
      destination[property] = source[property];
    }
  }
  return destination;
};

window.requestAnimFrame = (function(){
  return  window.requestAnimationFrame ||
    window.webkitRequestAnimationFrame ||
    window.mozRequestAnimationFrame    ||
    window.oRequestAnimationFrame      ||
    window.msRequestAnimationFrame     ||
    function(callback){
      window.setTimeout(callback, 1000 / 60);
    };
})();

window.cancelRequestAnimFrame = ( function() {
  return window.cancelAnimationFrame         ||
    window.webkitCancelRequestAnimationFrame ||
    window.mozCancelRequestAnimationFrame    ||
    window.oCancelRequestAnimationFrame      ||
    window.msCancelRequestAnimationFrame     ||
    clearTimeout
} )();

function hexToRgb(hex){
  // By Tim Down - http://stackoverflow.com/a/5624139/3493650
  // Expand shorthand form (e.g. "03F") to full form (e.g. "0033FF")
  var shorthandRegex = /^#?([a-f\d])([a-f\d])([a-f\d])$/i;
  hex = hex.replace(shorthandRegex, function(m, r, g, b) {
     return r + r + g + g + b + b;
  });
  var result = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(hex);
  return result ? {
      r: parseInt(result[1], 16),
      g: parseInt(result[2], 16),
      b: parseInt(result[3], 16)
  } : null;
};

function clamp(number, min, max) {
  return Math.min(Math.max(number, min), max);
};

function isInArray(value, array) {
  return array.indexOf(value) > -1;
}

/* ---------- particles.js functions - start ------------ */

window.pJSDom = [];
window.particlesJS = function(tag_id) {
  /* pJS elements */
  var pJS_tag = document.getElementById(tag_id),
      pJS_canvas_class = 'particles-js-canvas-el',
      exist_canvas = pJS_tag.getElementsByClassName(pJS_canvas_class);

  /* remove canvas if exists into the pJS target tag */
  if(exist_canvas.length){
    while(exist_canvas.length > 0){
      pJS_tag.removeChild(exist_canvas[0]);
    }
  }

  var canvas_el = document.createElement('canvas');
  canvas_el.className = pJS_canvas_class;
  canvas_el.style.width = "100%";
  canvas_el.style.height = "100%";

  var canvas = document.getElementById(tag_id).appendChild(canvas_el);
  if(canvas != null) { // ??
    pJSDom.push(new pJS(tag_id));
  }
};

document.addEventListener("DOMContentLoaded", (event) => {
  window.particlesJS('particles');
});