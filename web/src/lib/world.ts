import { feature } from 'topojson-client';
import { geoEquirectangular } from 'd3-geo';
import { numericToAlpha2 } from 'i18n-iso-countries';
import type { MapSeriesOption } from 'echarts/charts';
import type { GeometryCollection, Topology } from 'topojson-specification';
import type { FeatureCollection, Geometry } from 'geojson';

export function worldProjection(): NonNullable<MapSeriesOption['projection']> {
  const projection = geoEquirectangular().scale(1).translate([0, 0]);
  return {
    project: ([longitude, latitude]) => projection([longitude, latitude])!,
    unproject: ([x, y]) => projection.invert!([x, y])!,
    // The stream cuts polygons at ±180°; point projection alone draws their
    // boundary crossings as straight lines across the entire map.
    stream: (output) => projection.stream(output) as typeof output,
  };
}

export async function loadWorld(): Promise<FeatureCollection<Geometry>> {
  const module = await import('world-atlas/countries-110m.json');
  const topology = module.default as unknown as Topology<{ countries: GeometryCollection }>;
  const countries = feature(topology, topology.objects.countries);
  return {
    type: 'FeatureCollection',
    features: countries.features
      .filter((item) => String(item.id) !== '010')
      .map((item) => ({
        ...item,
        properties: {
          ...item.properties,
          name: numericToAlpha2(String(item.id).padStart(3, '0')) || String(item.id),
        },
      })),
  };
}
export function countryName(code: string): string {
  if (!/^[a-z]{2}$/i.test(code)) return code || 'Unknown';
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' }).of(code.toUpperCase()) || code;
  } catch {
    return code;
  }
}
