// Row shapes of the local store as the pages read them (columns of
// lib/local/schema.ts; TEXT -> string, INTEGER/REAL -> number, unset -> null).

export type ApiaryRow = {
  id: string;
  organization_id: string;
  name: string;
  address: string | null;
  lat: number;
  lng: number;
  note: string | null;
  created_at: string;
  updated_at: string;
};

export type HiveRow = {
  id: string;
  organization_id: string;
  apiary_id: string;
  name: string;
  type: number | null;
  status: number | null;
  boxes: number | null;
  colony_origin: string | null;
  note: string | null;
  qr_code: string | null;
  photo: string | null;
  created_at: string;
  updated_at: string;
};

export type QueenRow = {
  id: string;
  hive_id: string;
  year: number;
  marking: number;
  origin: string | null;
  breeder_number: string | null;
  introduced_at: string;
  replaced_at: string | null;
  active: number;
  note: string | null;
};

export type InspectionRow = {
  id: string;
  hive_id: string;
  date: string;
  weather: string | null;
  queen_seen: number | null;
  eggs_seen: number | null;
  temperament: number | null;
  frames: number | null;
  stores: number | null;
  queen_cells: number | null;
  varroa: string | null;
  honey_kg: number | null;
  note: string | null;
  brood_frames: number | null;
  calmness: number | null;
  fed_kg: number | null;
  frames_added: number | null;
  frames_removed: number | null;
  drone_frame_cut: number | null;
  super_added: number | null;
  weight_kg: number | null;
  youngest_larva: number | null;
  covered_larva: number | null;
  temp_hive: number | null;
  temp_outside: number | null;
  humidity_hive: number | null;
  humidity_outside: number | null;
  created_at: string;
  // Resolved from the photo_keys OR-Set by inspections.listByHive.
  photos: string[];
};

export type TaskRow = {
  id: string;
  organization_id: string;
  title: string;
  hive_id: string | null;
  apiary_id: string;
  due_at: string | null;
  done: number;
  priority: number | null;
  note: string | null;
  created_at: string;
};

// hives.locationHistory joins the apiary name onto the placement interval.
export type PlacementRow = {
  id: string;
  apiary_id: string;
  start_at: string;
  end_at: string | null;
  apiary_name: string | null;
};

export type HarvestRow = {
  id: string;
  hive_id: string;
  date: string;
  variety: string | null;
  amount_kg: number;
  water_content: number | null;
  batch_number: string | null;
  best_before: string | null;
  note: string | null;
};

export type TreatmentRow = {
  id: string;
  hive_id: string;
  date: string;
  product: string;
  active_ingredient: string | null;
  dose: string | null;
  method: string | null;
  batch_number: string | null;
  withdrawal_until: string | null;
  reason: string | null;
  note: string | null;
};
