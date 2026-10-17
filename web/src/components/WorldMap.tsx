import { useEffect, useMemo, useState } from 'react';
import { Chart, echarts, palette } from './Chart';
import type { ChartOption } from './Chart';
import { Loading, LoadError } from './ui';
import { countryName, loadWorld, worldProjection } from '../lib/world';
import { number } from '../lib/format';
import { errorMessage } from '../api';
import type { GeoPoint } from '../types';

let registered = false;
export default function WorldMap({
  points,
  onCountry,
  available,
}: {
  points: GeoPoint[];
  onCountry: (code: string) => void;
  available?: boolean;
}) {
  const [ready, setReady] = useState(registered);
  const [error, setError] = useState('');
  useEffect(() => {
    if (registered) return;
    let active = true;
    loadWorld()
      .then((geo) => {
        echarts.registerMap('mycdn-world', geo as Parameters<typeof echarts.registerMap>[1]);
        registered = true;
        if (active) setReady(true);
      })
      .catch((error) => {
        if (active) setError(errorMessage(error));
      });
    return () => {
      active = false;
    };
  }, []);
  const option = useMemo<ChartOption>(() => {
    const known = points.filter((point) => /^[A-Z]{2}$/i.test(point.country));
    const max = Math.max(1, ...known.map((point) => point.requests));
    return {
      tooltip: {
        trigger: 'item',
        formatter: (params) => {
          const value = Array.isArray(params) ? params[0] : params;
          return `${countryName(value.name || '')}: ${number(typeof value.value === 'number' ? value.value : undefined)} requests`;
        },
      },
      visualMap: {
        show: known.length > 0,
        min: 0,
        max,
        left: 12,
        bottom: 4,
        itemHeight: 80,
        itemWidth: 7,
        itemSymbol: 'rect',
        text: ['MORE', 'LESS'],
        textStyle: { color: '#9298a1', fontSize: 9 },
        calculable: false,
        type: 'piecewise',
        splitNumber: 5,
        inRange: { color: ['#292c30', '#743e32', palette[0]] },
      },
      series: [
        {
          id: 'countries',
          type: 'map',
          map: 'mycdn-world',
          projection: worldProjection(),
          roam: true,
          scaleLimit: { min: 1, max: 6 },
          left: 10,
          right: 10,
          top: 10,
          bottom: 10,
          preserveAspect: 'contain',
          nameProperty: 'name',
          selectedMode: false,
          itemStyle: { areaColor: '#1d2024', borderColor: '#444950', borderWidth: 0.5 },
          emphasis: {
            label: { show: true, formatter: (params) => countryName(params.name), color: '#fff' },
            itemStyle: { areaColor: '#b8482d', borderColor: '#ff5a36' },
          },
          data: known.map((point) => ({
            name: point.country.toUpperCase(),
            value: point.requests,
          })),
        },
      ],
    };
  }, [points]);
  if (error) return <LoadError error={error} />;
  if (!ready) return <Loading label="Loading offline map…" />;
  const hasCountries = points.some((point) => /^[A-Z]{2}$/i.test(point.country));
  return (
    <>
      <p className="footnote world-map-hint">
        {hasCountries
          ? 'Select a country to filter requests.'
          : available === false
            ? 'Country data unavailable · configure a country MMDB in Settings.'
            : 'No country data in this range.'}
      </p>
      <Chart
        height={305}
        option={option}
        label="World map of requests by country. Use country ranking below for keyboard filtering."
        onClick={(event) => {
          if (/^[A-Z]{2}$/.test(event.name)) onCountry(event.name);
        }}
      />
      <div className="chart-footnote">
        Scroll to zoom · drag to pan · select a country to filter{' '}
        <span>Natural Earth / world-atlas · offline</span>
      </div>
    </>
  );
}
