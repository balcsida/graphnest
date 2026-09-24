export interface Drawable {
  draw(): string;
}

export abstract class Shape implements Drawable {
  draw(): string {
    return 'shape';
  }
  area(): number {
    return 0;
  }
}

export class Square extends Shape {
  draw(): string {
    return 'square';
  }
}

export class Tile extends Square {
  label = 'tile';
}
