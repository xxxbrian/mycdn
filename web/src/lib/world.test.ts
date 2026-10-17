import { describe, expect, it } from 'vitest';
import { geoStream } from 'd3-geo';
import { loadWorld, worldProjection } from './world';

describe('offline geographic reference', () => {
  it('loads real country polygons and joins ISO codes without remote assets', async () => {
    const world = await loadWorld();
    expect(world.type).toBe('FeatureCollection');
    expect(world.features.length).toBeGreaterThan(150);
    for (const code of ['US', 'CA', 'CN', 'DE', 'BR', 'JP', 'AU', 'ZA']) {
      const country = world.features.find((feature) => feature.properties?.name === code);
      expect(country, code).toBeDefined();
      expect(['Polygon', 'MultiPolygon']).toContain(country!.geometry.type);
    }
    expect(world.features.some((feature) => feature.properties?.name === 'AQ')).toBe(false);
  });

  it('clips antimeridian crossings instead of connecting Fiji and Russia across the world', async () => {
    const world = await loadWorld();
    const projection = worldProjection();
    for (const code of ['FJ', 'RU']) {
      const country = world.features.find((feature) => feature.properties?.name === code)!;
      const rings: number[][][] = [];
      let ring: number[][];
      geoStream(
        country,
        projection.stream!({
          polygonStart() {},
          polygonEnd() {},
          lineStart() {
            ring = [];
          },
          lineEnd() {
            rings.push(ring);
          },
          point(x, y) {
            ring.push([x, y]);
          },
          sphere() {},
        }),
      );
      expect(rings.length).toBeGreaterThan(0);
      for (const points of rings) {
        for (let i = 0; i < points.length; i++) {
          const next = points[(i + 1) % points.length];
          expect(Math.abs(points[i][0] - next[0]), `${code} has a cross-map edge`).toBeLessThan(
            Math.PI,
          );
        }
      }
    }
    for (const point of [
      [-179.9, -16],
      [179.9, 69],
      [139.7, 35.7],
    ]) {
      const restored = projection.unproject(projection.project(point));
      expect(restored[0]).toBeCloseTo(point[0]);
      expect(restored[1]).toBeCloseTo(point[1]);
    }
  });
});
